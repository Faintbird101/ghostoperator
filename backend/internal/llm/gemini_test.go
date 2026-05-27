package llm_test

import (
	"fmt"
	"ghostoperator/backend/internal/llm"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func newTestClient(baseURL string) *llm.Client {
	return llm.NewClientWithBase("test-key", baseURL)
}

// result holds everything collected from one Stream call.
type result struct {
	texts      []string
	errs       []string
	errCode    string
	retryAfter int
	tokensUsed int
	maxTokens  int
}

func collectAll(t *testing.T, c *llm.Client, img []byte, opts llm.StreamOptions) result {
	t.Helper()
	out := make(chan llm.StreamChunk)
	go c.Stream(img, "image/png", opts, out)
	var r result
	for chunk := range out {
		switch {
		case chunk.Err != nil:
			r.errs = append(r.errs, chunk.Err.Error())
			r.errCode = chunk.ErrCode
			r.retryAfter = chunk.RetryAfter
		case chunk.Usage:
			r.tokensUsed = chunk.TokensUsed
			r.maxTokens = chunk.MaxTokens
		case chunk.Text != "":
			r.texts = append(r.texts, chunk.Text)
		}
	}
	return r
}

// ── Happy path ────────────────────────────────────────────────────────────────

func TestGeminiClient_SuccessfulStream(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"candidates\":[{\"content\":{\"parts\":[{\"text\":\"Hey \"}]}}]}\n\n")
		fmt.Fprint(w, "data: {\"candidates\":[{\"content\":{\"parts\":[{\"text\":\"there!\"}]}}],\"usageMetadata\":{\"candidatesTokenCount\":2}}\n\n")
	}))
	defer server.Close()

	r := collectAll(t, newTestClient(server.URL), []byte("img"), llm.StreamOptions{})

	if len(r.errs) != 0 {
		t.Fatalf("unexpected errors: %v", r.errs)
	}
	if got := strings.Join(r.texts, ""); got != "Hey there!" {
		t.Errorf("text: want %q, got %q", "Hey there!", got)
	}
	if r.tokensUsed != 2 {
		t.Errorf("tokensUsed: want 2, got %d", r.tokensUsed)
	}
	if r.maxTokens != 512 {
		t.Errorf("maxTokens: want 512, got %d", r.maxTokens)
	}
}

func TestGeminiClient_UsageMetadata_EmittedAsUsageChunk(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"candidates\":[{\"content\":{\"parts\":[{\"text\":\"hi\"}]}}],\"usageMetadata\":{\"candidatesTokenCount\":99}}\n\n")
	}))
	defer server.Close()

	r := collectAll(t, newTestClient(server.URL), []byte("img"), llm.StreamOptions{})
	if r.tokensUsed != 99 {
		t.Errorf("want tokensUsed 99, got %d", r.tokensUsed)
	}
}

// ── Error handling ────────────────────────────────────────────────────────────

func TestGeminiClient_APIErrorStatus(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		fmt.Fprint(w, `{"error":{"message":"API key not valid"}}`)
	}))
	defer server.Close()

	r := collectAll(t, newTestClient(server.URL), []byte("img"), llm.StreamOptions{})
	if len(r.errs) == 0 {
		t.Fatal("expected an error chunk")
	}
	if !strings.Contains(r.errs[0], "401") {
		t.Errorf("expected 401 in error, got %q", r.errs[0])
	}
}

func TestGeminiClient_RateLimitError_SetsErrCode(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
		fmt.Fprint(w, `{"error":{"details":[{"@type":"type.googleapis.com/google.rpc.RetryInfo","retryDelay":"32.5s"}]}}`)
	}))
	defer server.Close()

	r := collectAll(t, newTestClient(server.URL), []byte("img"), llm.StreamOptions{})
	if len(r.errs) == 0 {
		t.Fatal("expected an error chunk")
	}
	if r.errCode != "RATE_LIMIT" {
		t.Errorf("errCode: want RATE_LIMIT, got %q", r.errCode)
	}
	if r.retryAfter != 33 { // ceil(32.5) = 33
		t.Errorf("retryAfter: want 33, got %d", r.retryAfter)
	}
}

func TestGeminiClient_RateLimitError_ExactSeconds(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
		fmt.Fprint(w, `{"error":{"details":[{"@type":"type.googleapis.com/google.rpc.RetryInfo","retryDelay":"60s"}]}}`)
	}))
	defer server.Close()

	r := collectAll(t, newTestClient(server.URL), []byte("img"), llm.StreamOptions{})
	if r.retryAfter != 60 {
		t.Errorf("retryAfter: want 60, got %d", r.retryAfter)
	}
}

func TestGeminiClient_RateLimitError_FallbackWhenNoRetryInfo(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
		fmt.Fprint(w, `{"error":{"message":"quota exceeded"}}`)
	}))
	defer server.Close()

	r := collectAll(t, newTestClient(server.URL), []byte("img"), llm.StreamOptions{})
	if r.errCode != "RATE_LIMIT" {
		t.Errorf("errCode: want RATE_LIMIT, got %q", r.errCode)
	}
	if r.retryAfter != 60 { // default fallback
		t.Errorf("retryAfter fallback: want 60, got %d", r.retryAfter)
	}
}

// ── SSE parsing edge cases ────────────────────────────────────────────────────

func TestGeminiClient_MalformedSSELinesAreSkipped(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "not-a-data-line\n\n")
		fmt.Fprint(w, "data: {invalid json}\n\n")
		fmt.Fprint(w, "data: {\"candidates\":[{\"content\":{\"parts\":[{\"text\":\"OK\"}]}}]}\n\n")
	}))
	defer server.Close()

	r := collectAll(t, newTestClient(server.URL), []byte("img"), llm.StreamOptions{})
	if len(r.errs) != 0 {
		t.Fatalf("garbage lines should be skipped, got errors: %v", r.errs)
	}
	if strings.Join(r.texts, "") != "OK" {
		t.Errorf("want %q, got %q", "OK", strings.Join(r.texts, ""))
	}
}

func TestGeminiClient_EmptyPartsAreIgnored(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"candidates\":[{\"content\":{\"parts\":[]}}]}\n\n")
		fmt.Fprint(w, "data: {\"candidates\":[{\"content\":{\"parts\":[{\"text\":\"hi\"}]}}]}\n\n")
	}))
	defer server.Close()

	r := collectAll(t, newTestClient(server.URL), []byte("img"), llm.StreamOptions{})
	if len(r.errs) != 0 {
		t.Fatalf("unexpected errors: %v", r.errs)
	}
	if strings.Join(r.texts, "") != "hi" {
		t.Errorf("want %q, got %q", "hi", strings.Join(r.texts, ""))
	}
}

// ── Request shape ─────────────────────────────────────────────────────────────

func TestGeminiClient_SendsAPIKeyInURL(t *testing.T) {
	var capturedURL string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedURL = r.URL.String()
		w.Header().Set("Content-Type", "text/event-stream")
	}))
	defer server.Close()

	collectAll(t, newTestClient(server.URL), []byte("img"), llm.StreamOptions{})
	if !strings.Contains(capturedURL, "key=test-key") {
		t.Errorf("API key not in URL: %q", capturedURL)
	}
}

func TestGeminiClient_DefaultModel_UsedInURL(t *testing.T) {
	var capturedURL string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedURL = r.URL.String()
		w.Header().Set("Content-Type", "text/event-stream")
	}))
	defer server.Close()

	collectAll(t, newTestClient(server.URL), []byte("img"), llm.StreamOptions{})
	if !strings.Contains(capturedURL, "gemini-2.5-flash") {
		t.Errorf("default model not in URL: %q", capturedURL)
	}
}

func TestGeminiClient_CustomModel_UsedInURL(t *testing.T) {
	var capturedURL string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedURL = r.URL.String()
		w.Header().Set("Content-Type", "text/event-stream")
	}))
	defer server.Close()

	collectAll(t, newTestClient(server.URL), []byte("img"), llm.StreamOptions{Model: "gemini-2.5-flash-lite"})
	if !strings.Contains(capturedURL, "gemini-2.5-flash-lite") {
		t.Errorf("custom model not in URL: %q", capturedURL)
	}
}
