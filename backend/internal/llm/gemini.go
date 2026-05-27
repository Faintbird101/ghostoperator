package llm

import (
	"bufio"
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

const defaultModel    = "gemini-2.5-flash"
const maxOutputTokens = 512

const systemPrompt = `You are a reply assistant for a chat platform.

The screenshot shows a three-panel interface:
- Left panel: the customer — their name and any visible profile details
- Center panel: the chat conversation between the customer and the persona
- Right panel: the persona you are replying as — their name, personality, tone, and details

Your job: read all three panels and write the next reply from the persona's side.

Rules:
- Match the persona's natural voice, tone, and style exactly as shown in their profile
- Match the energy of the conversation — mirror its pace, warmth, and formality level
- Keep replies concise and natural unless the conversation clearly calls for more
- Never break character, never explain yourself, never add labels or metadata
- Output only the reply text — nothing else`

// StreamOptions controls the behaviour of a single Stream call.
type StreamOptions struct {
	Model        string // empty → defaultModel
	CustomPrompt string // appended to the user message when set
}

// StreamChunk is one unit of streaming output from the LLM.
type StreamChunk struct {
	Text       string
	Err        error
	ErrCode    string // "RATE_LIMIT" | "" — set alongside Err
	RetryAfter int    // seconds until retry; set when ErrCode == "RATE_LIMIT"
	Usage      bool   // true for the final usage-metadata chunk
	TokensUsed int    // output tokens consumed (set when Usage == true)
	MaxTokens  int    // output token cap    (set when Usage == true)
}

type Client struct {
	apiKey     string
	httpClient *http.Client
	baseURL    string
}

func NewClient(apiKey string) *Client {
	return NewClientWithBase(apiKey, "https://generativelanguage.googleapis.com")
}

// NewClientWithBase lets tests inject a mock server URL.
func NewClientWithBase(apiKey, baseURL string) *Client {
	return &Client{
		apiKey:     apiKey,
		httpClient: &http.Client{},
		baseURL:    baseURL,
	}
}

type geminiRequest struct {
	SystemInstruction geminiContent   `json:"systemInstruction"`
	Contents          []geminiContent `json:"contents"`
	GenerationConfig  map[string]any  `json:"generationConfig"`
}

type geminiContent struct {
	Parts []geminiPart `json:"parts"`
	Role  string       `json:"role,omitempty"`
}

type geminiPart struct {
	Text       string        `json:"text,omitempty"`
	InlineData *geminiInline `json:"inlineData,omitempty"`
}

type geminiInline struct {
	MimeType string `json:"mimeType"`
	Data     string `json:"data"`
}

func (c *Client) Stream(imageData []byte, mediaType string, opts StreamOptions, out chan<- StreamChunk) {
	defer close(out)

	model := opts.Model
	if model == "" {
		model = defaultModel
	}

	userText := "Write the next reply from the persona shown on the right side of this screenshot."
	if opts.CustomPrompt != "" {
		userText += "\n\nAdditional instructions: " + opts.CustomPrompt
	}

	encoded := base64.StdEncoding.EncodeToString(imageData)

	payload := geminiRequest{
		SystemInstruction: geminiContent{
			Parts: []geminiPart{{Text: systemPrompt}},
		},
		Contents: []geminiContent{
			{
				Role: "user",
				Parts: []geminiPart{
					{InlineData: &geminiInline{MimeType: mediaType, Data: encoded}},
					{Text: userText},
				},
			},
		},
		GenerationConfig: map[string]any{
			"maxOutputTokens": maxOutputTokens,
		},
	}

	body, err := json.Marshal(payload)
	if err != nil {
		out <- StreamChunk{Err: fmt.Errorf("encode request: %w", err)}
		return
	}

	url := fmt.Sprintf(
		"%s/v1beta/models/%s:streamGenerateContent?alt=sse&key=%s",
		c.baseURL, model, c.apiKey,
	)

	req, err := http.NewRequest("POST", url, bytes.NewReader(body))
	if err != nil {
		out <- StreamChunk{Err: fmt.Errorf("create request: %w", err)}
		return
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		out <- StreamChunk{Err: fmt.Errorf("API call: %w", err)}
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		errBody, _ := io.ReadAll(resp.Body)
		if resp.StatusCode == http.StatusTooManyRequests {
			out <- StreamChunk{
				Err:        fmt.Errorf("rate limit exceeded"),
				ErrCode:    "RATE_LIMIT",
				RetryAfter: parseRetryAfter(errBody),
			}
			return
		}
		out <- StreamChunk{Err: fmt.Errorf("API error %d: %s", resp.StatusCode, errBody)}
		return
	}

	scanner := bufio.NewScanner(resp.Body)
	// Increase scanner buffer for large image payloads in responses
	scanner.Buffer(make([]byte, 512*1024), 1024*1024)

	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		data := strings.TrimPrefix(line, "data: ")

		var event struct {
			Candidates []struct {
				Content struct {
					Parts []struct {
						Text string `json:"text"`
					} `json:"parts"`
				} `json:"content"`
				FinishReason string `json:"finishReason"`
			} `json:"candidates"`
			UsageMetadata *struct {
				CandidatesTokenCount int `json:"candidatesTokenCount"`
			} `json:"usageMetadata"`
		}
		if err := json.Unmarshal([]byte(data), &event); err != nil {
			continue
		}

		for _, candidate := range event.Candidates {
			for _, part := range candidate.Content.Parts {
				if part.Text != "" {
					out <- StreamChunk{Text: part.Text}
				}
			}
		}

		if event.UsageMetadata != nil {
			out <- StreamChunk{
				Usage:      true,
				TokensUsed: event.UsageMetadata.CandidatesTokenCount,
				MaxTokens:  maxOutputTokens,
			}
		}
	}
}

// ─── OCR / thread extraction ─────────────────────────────────────────────────

const ocrSystemPrompt = `Extract all chat messages visible in this screenshot.
Return ONLY a JSON array with this exact format:
[{"who":"them","text":"message text","t":"timestamp if visible"}]
Rules:
- "who" is "you" for the right-aligned / own messages, "them" for the other person
- Include every message in order, top to bottom
- "t" is the timestamp string if visible, otherwise empty string ""
- Return only the JSON array — no markdown fences, no explanation`

// ThreadMessage is one parsed message from an OCR'd chat screenshot.
type ThreadMessage struct {
	Who  string `json:"who"`
	Text string `json:"text"`
	T    string `json:"t"`
}

// ExtractThread calls Gemini Vision to parse a chat screenshot into messages.
func (c *Client) ExtractThread(imageData []byte, mediaType string, model string) ([]ThreadMessage, error) {
	if model == "" {
		model = defaultModel
	}

	encoded := base64.StdEncoding.EncodeToString(imageData)

	payload := geminiRequest{
		SystemInstruction: geminiContent{
			Parts: []geminiPart{{Text: ocrSystemPrompt}},
		},
		Contents: []geminiContent{{
			Role: "user",
			Parts: []geminiPart{
				{InlineData: &geminiInline{MimeType: mediaType, Data: encoded}},
				{Text: "Extract the chat messages from this screenshot. Return only the JSON array."},
			},
		}},
		GenerationConfig: map[string]any{
			"maxOutputTokens": 1024,
			"temperature":     0,
		},
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("encode request: %w", err)
	}

	url := fmt.Sprintf(
		"%s/v1beta/models/%s:generateContent?key=%s",
		c.baseURL, model, c.apiKey,
	)

	req, err := http.NewRequest("POST", url, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("API call: %w", err)
	}
	defer resp.Body.Close()

	respBody, _ := io.ReadAll(resp.Body)

	if resp.StatusCode != http.StatusOK {
		if resp.StatusCode == http.StatusTooManyRequests {
			return nil, fmt.Errorf("rate limit exceeded")
		}
		return nil, fmt.Errorf("API error %d: %s", resp.StatusCode, respBody)
	}

	var result struct {
		Candidates []struct {
			Content struct {
				Parts []struct {
					Text string `json:"text"`
				} `json:"parts"`
			} `json:"content"`
		} `json:"candidates"`
	}
	if err := json.Unmarshal(respBody, &result); err != nil {
		return nil, fmt.Errorf("decode response: %w", err)
	}
	if len(result.Candidates) == 0 || len(result.Candidates[0].Content.Parts) == 0 {
		return nil, fmt.Errorf("no content in response")
	}

	text := strings.TrimSpace(result.Candidates[0].Content.Parts[0].Text)
	// Strip markdown fences Gemini sometimes adds
	text = strings.TrimPrefix(text, "```json")
	text = strings.TrimPrefix(text, "```")
	text = strings.TrimSuffix(text, "```")
	text = strings.TrimSpace(text)

	var thread []ThreadMessage
	if err := json.Unmarshal([]byte(text), &thread); err != nil {
		return nil, fmt.Errorf("parse thread JSON: %w (raw: %.200s)", err, text)
	}
	return thread, nil
}

// ─── Draft reply generation ───────────────────────────────────────────────────

// DraftOptions controls multi-tone reply generation.
type DraftOptions struct {
	Model   string
	Tones   []string
	Length  string // "shorter" | "default" | "longer"
	Context string
}

// DraftChunk is one unit of streaming output from DraftReplies.
type DraftChunk struct {
	ID         string  `json:"id,omitempty"`
	Tone       string  `json:"tone,omitempty"`
	Streaming  bool    `json:"streaming,omitempty"`
	Delta      string  `json:"delta,omitempty"`
	Done       bool    `json:"done,omitempty"`
	Tokens     int     `json:"tokens,omitempty"`
	Latency    string  `json:"latency,omitempty"`
	Confidence float64 `json:"confidence,omitempty"`
	Err        error   `json:"-"`
	ErrCode    string  `json:"errCode,omitempty"`
}

// DraftReplies generates one reply per tone in parallel, streaming chunks to out.
func (c *Client) DraftReplies(thread []ThreadMessage, opts DraftOptions, out chan<- DraftChunk) {
	defer close(out)

	model := opts.Model
	if model == "" {
		model = defaultModel
	}

	tones := opts.Tones
	if len(tones) == 0 {
		tones = []string{"Witty", "Dry", "Sincere"}
	}
	if len(tones) > 4 {
		tones = tones[:4]
	}

	threadText := buildThreadText(thread)

	var wg sync.WaitGroup
	for i, tone := range tones {
		wg.Add(1)
		go func(idx int, tn string) {
			defer wg.Done()
			id := fmt.Sprintf("c%d-%s", idx, strings.ToLower(tn))

			out <- DraftChunk{ID: id, Tone: tn, Streaming: true}

			start := time.Now()
			streamCh := make(chan StreamChunk, 32)
			go c.streamDraft(threadText, tn, opts.Length, opts.Context, model, streamCh)

			var sb strings.Builder
			var tokens int
			for chunk := range streamCh {
				if chunk.Err != nil {
					out <- DraftChunk{ID: id, Err: chunk.Err, ErrCode: chunk.ErrCode}
					return
				}
				if chunk.Usage {
					tokens = chunk.TokensUsed
					continue
				}
				if chunk.Text != "" {
					sb.WriteString(chunk.Text)
					out <- DraftChunk{ID: id, Delta: chunk.Text}
				}
			}

			elapsed := time.Since(start)
			latency := fmt.Sprintf("%.2f", elapsed.Seconds())

			// Pseudo-confidence based on content hash — stable for same text
			text := sb.String()
			hashVal := 0
			for _, ch := range text {
				hashVal += int(ch)
			}
			conf := 0.72 + float64(hashVal%23)/100.0
			if conf > 0.94 {
				conf = 0.94
			}
			// Round to 2 decimal places for clean JSON output
			conf = float64(int(conf*100+0.5)) / 100.0

			out <- DraftChunk{
				ID: id, Done: true,
				Tokens: tokens, Latency: latency, Confidence: conf,
			}
		}(i, tone)
	}

	wg.Wait()
}

func buildThreadText(thread []ThreadMessage) string {
	var sb strings.Builder
	for _, m := range thread {
		ts := ""
		if m.T != "" {
			ts = "[" + m.T + "] "
		}
		sb.WriteString(ts + m.Who + ": " + m.Text + "\n")
	}
	return strings.TrimSpace(sb.String())
}

func (c *Client) streamDraft(threadText, tone, length, context, model string, out chan<- StreamChunk) {
	defer close(out)

	lengthHint := map[string]string{
		"shorter": "Keep it brief — under 15 words if possible.",
		"default": "Natural conversational length.",
		"longer":  "More expansive than usual — 2-3 sentences.",
	}[length]
	if lengthHint == "" {
		lengthHint = "Natural conversational length."
	}

	toneDesc := map[string]string{
		"Witty":   "Clever and playful with light humour. Quick, sharp, fun.",
		"Dry":     "Understated and deadpan. Minimal emotion, maximum cool.",
		"Sincere": "Genuine, warm, and direct. Authentic without being sappy.",
		"Concise": "Minimal words. Maximum effect. Strip it right down.",
		"Playful": "Upbeat and fun. Energy and enthusiasm — emoji welcome.",
	}[tone]
	if toneDesc == "" {
		toneDesc = "Natural and conversational."
	}

	sysPrompt := fmt.Sprintf(
		"You are a reply assistant. Write the next message in a chat conversation.\nTone: %s\n%s\nOutput only the reply text — no quotes, no labels, no explanation.",
		toneDesc, lengthHint,
	)

	userMsg := "Chat thread:\n" + threadText
	if context != "" {
		userMsg += "\n\nAdditional context: " + context
	}
	userMsg += "\n\nWrite the next reply."

	payload := geminiRequest{
		SystemInstruction: geminiContent{
			Parts: []geminiPart{{Text: sysPrompt}},
		},
		Contents: []geminiContent{{
			Role: "user",
			Parts: []geminiPart{
				{Text: userMsg},
			},
		}},
		GenerationConfig: map[string]any{
			"maxOutputTokens": 256,
			"temperature":     0.9,
		},
	}

	body, err := json.Marshal(payload)
	if err != nil {
		out <- StreamChunk{Err: fmt.Errorf("encode request: %w", err)}
		return
	}

	url := fmt.Sprintf(
		"%s/v1beta/models/%s:streamGenerateContent?alt=sse&key=%s",
		c.baseURL, model, c.apiKey,
	)

	req, err := http.NewRequest("POST", url, bytes.NewReader(body))
	if err != nil {
		out <- StreamChunk{Err: fmt.Errorf("create request: %w", err)}
		return
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		out <- StreamChunk{Err: fmt.Errorf("API call: %w", err)}
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		errBody, _ := io.ReadAll(resp.Body)
		if resp.StatusCode == http.StatusTooManyRequests {
			out <- StreamChunk{
				Err:        fmt.Errorf("rate limit exceeded"),
				ErrCode:    "RATE_LIMIT",
				RetryAfter: parseRetryAfter(errBody),
			}
			return
		}
		out <- StreamChunk{Err: fmt.Errorf("API error %d: %s", resp.StatusCode, errBody)}
		return
	}

	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 512*1024), 1024*1024)

	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		data := strings.TrimPrefix(line, "data: ")

		var event struct {
			Candidates []struct {
				Content struct {
					Parts []struct {
						Text string `json:"text"`
					} `json:"parts"`
				} `json:"content"`
			} `json:"candidates"`
			UsageMetadata *struct {
				CandidatesTokenCount int `json:"candidatesTokenCount"`
			} `json:"usageMetadata"`
		}
		if err := json.Unmarshal([]byte(data), &event); err != nil {
			continue
		}

		for _, candidate := range event.Candidates {
			for _, part := range candidate.Content.Parts {
				if part.Text != "" {
					out <- StreamChunk{Text: part.Text}
				}
			}
		}

		if event.UsageMetadata != nil {
			out <- StreamChunk{
				Usage:      true,
				TokensUsed: event.UsageMetadata.CandidatesTokenCount,
				MaxTokens:  256,
			}
		}
	}
}

// parseRetryAfter extracts the retry delay in seconds from a Gemini 429 response body.
func parseRetryAfter(body []byte) int {
	var apiResp struct {
		Error struct {
			Details []json.RawMessage `json:"details"`
		} `json:"error"`
	}
	if json.Unmarshal(body, &apiResp) != nil {
		return 60
	}
	for _, raw := range apiResp.Error.Details {
		var detail struct {
			Type       string `json:"@type"`
			RetryDelay string `json:"retryDelay"`
		}
		if json.Unmarshal(raw, &detail) != nil {
			continue
		}
		if !strings.Contains(detail.Type, "RetryInfo") || detail.RetryDelay == "" {
			continue
		}
		s := strings.TrimSuffix(detail.RetryDelay, "s")
		if f, err := strconv.ParseFloat(s, 64); err == nil {
			secs := int(f)
			if f > float64(secs) {
				secs++ // ceil
			}
			return secs
		}
	}
	return 60
}
