package handler_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"ghostoperator/backend/internal/handler"
	"ghostoperator/backend/internal/llm"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// ── Mock streamers ────────────────────────────────────────────────────────────

type mockStreamer struct {
	chunks []llm.StreamChunk
}

func (m *mockStreamer) Stream(_ []byte, _ string, _ llm.StreamOptions, out chan<- llm.StreamChunk) {
	defer close(out)
	for _, c := range m.chunks {
		out <- c
	}
}

// capturingStreamer records the StreamOptions it receives.
type capturingStreamer struct {
	opts     chan llm.StreamOptions
	media    chan string
}

func (c *capturingStreamer) Stream(_ []byte, mediaType string, opts llm.StreamOptions, out chan<- llm.StreamChunk) {
	defer close(out)
	c.opts <- opts
	c.media <- mediaType
}

// ── Helpers ───────────────────────────────────────────────────────────────────

func buildMultipart(t *testing.T, fieldName, filename string, content []byte) (*bytes.Buffer, string) {
	t.Helper()
	body := &bytes.Buffer{}
	w := multipart.NewWriter(body)
	fw, err := w.CreateFormFile(fieldName, filename)
	if err != nil {
		t.Fatalf("create form file: %v", err)
	}
	fw.Write(content)
	w.Close()
	return body, w.FormDataContentType()
}

func buildMultipartWithFields(t *testing.T, imgField, filename string, content []byte, extra map[string]string) (*bytes.Buffer, string) {
	t.Helper()
	body := &bytes.Buffer{}
	w := multipart.NewWriter(body)
	fw, err := w.CreateFormFile(imgField, filename)
	if err != nil {
		t.Fatalf("create form file: %v", err)
	}
	fw.Write(content)
	for k, v := range extra {
		w.WriteField(k, v)
	}
	w.Close()
	return body, w.FormDataContentType()
}

func collectSSE(t *testing.T, body io.Reader) []map[string]any {
	t.Helper()
	raw, err := io.ReadAll(body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	var events []map[string]any
	for _, line := range strings.Split(string(raw), "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		data := strings.TrimPrefix(line, "data: ")
		if data == "[DONE]" {
			events = append(events, map[string]any{"_done": true})
			continue
		}
		var m map[string]any
		if json.Unmarshal([]byte(data), &m) == nil {
			events = append(events, m)
		}
	}
	return events
}

var minimalPNG = []byte{
	0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a,
	0x00, 0x00, 0x00, 0x0d, 0x49, 0x48, 0x44, 0x52,
	0x00, 0x00, 0x00, 0x01, 0x00, 0x00, 0x00, 0x01,
	0x08, 0x06, 0x00, 0x00, 0x00, 0x1f, 0x15, 0xc4,
	0x89, 0x00, 0x00, 0x00, 0x0b, 0x49, 0x44, 0x41,
	0x54, 0x78, 0x9c, 0x62, 0x00, 0x01, 0x00, 0x00,
	0x05, 0x00, 0x01, 0x0d, 0x0a, 0x2d, 0xb4, 0x00,
	0x00, 0x00, 0x00, 0x49, 0x45, 0x4e, 0x44, 0xae,
	0x42, 0x60, 0x82,
}

// ── Core HTTP behaviour ───────────────────────────────────────────────────────

func TestReplyHandler_CORS_Preflight(t *testing.T) {
	h := handler.NewReplyHandler(&mockStreamer{})
	req := httptest.NewRequest(http.MethodOptions, "/reply", nil)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)

	if rr.Code != http.StatusNoContent {
		t.Errorf("want 204, got %d", rr.Code)
	}
	if got := rr.Header().Get("Access-Control-Allow-Origin"); got != "*" {
		t.Errorf("CORS origin: want *, got %q", got)
	}
	if got := rr.Header().Get("Access-Control-Allow-Methods"); !strings.Contains(got, "POST") {
		t.Errorf("CORS methods missing POST: %q", got)
	}
}

func TestReplyHandler_WrongMethod_GET(t *testing.T) {
	h := handler.NewReplyHandler(&mockStreamer{})
	req := httptest.NewRequest(http.MethodGet, "/reply", nil)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	if rr.Code != http.StatusMethodNotAllowed {
		t.Errorf("want 405, got %d", rr.Code)
	}
}

func TestReplyHandler_WrongMethod_PUT(t *testing.T) {
	h := handler.NewReplyHandler(&mockStreamer{})
	req := httptest.NewRequest(http.MethodPut, "/reply", nil)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	if rr.Code != http.StatusMethodNotAllowed {
		t.Errorf("want 405, got %d", rr.Code)
	}
}

func TestReplyHandler_MissingScreenshotField(t *testing.T) {
	h := handler.NewReplyHandler(&mockStreamer{})
	body, ct := buildMultipart(t, "wrong_field", "img.png", minimalPNG)
	req := httptest.NewRequest(http.MethodPost, "/reply", body)
	req.Header.Set("Content-Type", ct)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Errorf("want 400, got %d", rr.Code)
	}
}

func TestReplyHandler_EmptyBody(t *testing.T) {
	h := handler.NewReplyHandler(&mockStreamer{})
	req := httptest.NewRequest(http.MethodPost, "/reply", strings.NewReader(""))
	req.Header.Set("Content-Type", "multipart/form-data; boundary=xyz")
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Errorf("want 400, got %d", rr.Code)
	}
}

// ── Streaming behaviour ───────────────────────────────────────────────────────

func TestReplyHandler_SuccessfulStream(t *testing.T) {
	mock := &mockStreamer{chunks: []llm.StreamChunk{
		{Text: "Hello"},
		{Text: ", world"},
		{Text: "!"},
	}}
	h := handler.NewReplyHandler(mock)
	body, ct := buildMultipart(t, "screenshot", "shot.png", minimalPNG)
	req := httptest.NewRequest(http.MethodPost, "/reply", body)
	req.Header.Set("Content-Type", ct)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("want 200, got %d — %s", rr.Code, rr.Body.String())
	}
	events := collectSSE(t, rr.Body)
	if _, done := events[len(events)-1]["_done"]; !done {
		t.Error("last event should be [DONE]")
	}
	var combined string
	for _, ev := range events {
		if s, ok := ev["text"].(string); ok {
			combined += s
		}
	}
	if combined != "Hello, world!" {
		t.Errorf("combined text: want %q, got %q", "Hello, world!", combined)
	}
}

func TestReplyHandler_TokenUsage_EmitsTokenEvent(t *testing.T) {
	mock := &mockStreamer{chunks: []llm.StreamChunk{
		{Text: "hey"},
		{Usage: true, TokensUsed: 42, MaxTokens: 512},
	}}
	h := handler.NewReplyHandler(mock)
	body, ct := buildMultipart(t, "screenshot", "shot.png", minimalPNG)
	req := httptest.NewRequest(http.MethodPost, "/reply", body)
	req.Header.Set("Content-Type", ct)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)

	events := collectSSE(t, rr.Body)
	var tokenEvent map[string]any
	for _, ev := range events {
		if _, ok := ev["tokens"]; ok {
			tokenEvent = ev
			break
		}
	}
	if tokenEvent == nil {
		t.Fatal("no token event found in SSE stream")
	}
	if int(tokenEvent["tokens"].(float64)) != 42 {
		t.Errorf("tokens: want 42, got %v", tokenEvent["tokens"])
	}
	if int(tokenEvent["limit"].(float64)) != 512 {
		t.Errorf("limit: want 512, got %v", tokenEvent["limit"])
	}
}

func TestReplyHandler_LLMError_StreamsErrorEvent(t *testing.T) {
	mock := &mockStreamer{chunks: []llm.StreamChunk{
		{Err: fmt.Errorf("API error 429: rate limited")},
	}}
	h := handler.NewReplyHandler(mock)
	body, ct := buildMultipart(t, "screenshot", "shot.png", minimalPNG)
	req := httptest.NewRequest(http.MethodPost, "/reply", body)
	req.Header.Set("Content-Type", ct)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)

	events := collectSSE(t, rr.Body)
	if len(events) == 0 || events[0]["error"] == nil {
		t.Error("expected an error event in SSE stream")
	}
}

func TestReplyHandler_RateLimit_IncludesErrCodeAndRetryAfter(t *testing.T) {
	mock := &mockStreamer{chunks: []llm.StreamChunk{
		{Err: fmt.Errorf("rate limit exceeded"), ErrCode: "RATE_LIMIT", RetryAfter: 30},
	}}
	h := handler.NewReplyHandler(mock)
	body, ct := buildMultipart(t, "screenshot", "shot.png", minimalPNG)
	req := httptest.NewRequest(http.MethodPost, "/reply", body)
	req.Header.Set("Content-Type", ct)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)

	events := collectSSE(t, rr.Body)
	if len(events) == 0 {
		t.Fatal("expected at least one SSE event")
	}
	ev := events[0]
	if ev["errCode"] != "RATE_LIMIT" {
		t.Errorf("errCode: want RATE_LIMIT, got %v", ev["errCode"])
	}
	if int(ev["retryAfter"].(float64)) != 30 {
		t.Errorf("retryAfter: want 30, got %v", ev["retryAfter"])
	}
}

// ── Form field passthrough ────────────────────────────────────────────────────

func TestReplyHandler_PassesModelAndCustomPrompt(t *testing.T) {
	cap := &capturingStreamer{
		opts:  make(chan llm.StreamOptions, 1),
		media: make(chan string, 1),
	}
	h := handler.NewReplyHandler(cap)
	body, ct := buildMultipartWithFields(t, "screenshot", "shot.png", minimalPNG, map[string]string{
		"model":         "gemini-custom",
		"custom_prompt": "be brief",
	})
	req := httptest.NewRequest(http.MethodPost, "/reply", body)
	req.Header.Set("Content-Type", ct)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)

	opts := <-cap.opts
	if opts.Model != "gemini-custom" {
		t.Errorf("model: want gemini-custom, got %q", opts.Model)
	}
	if opts.CustomPrompt != "be brief" {
		t.Errorf("custom_prompt: want %q, got %q", "be brief", opts.CustomPrompt)
	}
}

func TestReplyHandler_MediaType_DefaultsToPNG(t *testing.T) {
	cap := &capturingStreamer{
		opts:  make(chan llm.StreamOptions, 1),
		media: make(chan string, 1),
	}
	h := handler.NewReplyHandler(cap)

	buf := &bytes.Buffer{}
	mw := multipart.NewWriter(buf)
	fw, _ := mw.CreateFormFile("screenshot", "shot.bin")
	fw.Write(minimalPNG)
	mw.Close()

	req := httptest.NewRequest(http.MethodPost, "/reply", buf)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)

	if mt := <-cap.media; mt != "image/png" {
		t.Errorf("media type: want image/png, got %q", mt)
	}
}
