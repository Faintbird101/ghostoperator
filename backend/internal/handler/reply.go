package handler

import (
	"encoding/json"
	"fmt"
	"ghostoperator/backend/internal/llm"
	"io"
	"net/http"
)

// Streamer is the interface the reply handler depends on.
// *llm.Client satisfies it; tests supply a mock.
type Streamer interface {
	Stream(imageData []byte, mediaType string, opts llm.StreamOptions, out chan<- llm.StreamChunk)
}

type ReplyHandler struct {
	llm Streamer
}

func NewReplyHandler(llmClient Streamer) *ReplyHandler {
	return &ReplyHandler{llm: llmClient}
}

func (h *ReplyHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodOptions {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "POST, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
		w.WriteHeader(http.StatusNoContent)
		return
	}

	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	if err := r.ParseMultipartForm(20 << 20); err != nil {
		http.Error(w, "failed to parse form (max 20MB)", http.StatusBadRequest)
		return
	}

	file, header, err := r.FormFile("screenshot")
	if err != nil {
		http.Error(w, "missing 'screenshot' field", http.StatusBadRequest)
		return
	}
	defer file.Close()

	imgData, err := io.ReadAll(file)
	if err != nil {
		http.Error(w, "failed to read image", http.StatusInternalServerError)
		return
	}

	mediaType := header.Header.Get("Content-Type")
	if mediaType == "" || mediaType == "application/octet-stream" {
		mediaType = "image/png"
	}

	opts := llm.StreamOptions{
		Model:        r.FormValue("model"),
		CustomPrompt: r.FormValue("custom_prompt"),
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Access-Control-Allow-Origin", "*")

	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming not supported", http.StatusInternalServerError)
		return
	}

	chunks := make(chan llm.StreamChunk)
	go h.llm.Stream(imgData, mediaType, opts, chunks)

	for chunk := range chunks {
		var data []byte

		switch {
		case chunk.Err != nil:
			m := map[string]any{"error": chunk.Err.Error()}
			if chunk.ErrCode != "" {
				m["errCode"] = chunk.ErrCode
			}
			if chunk.RetryAfter > 0 {
				m["retryAfter"] = chunk.RetryAfter
			}
			data, _ = json.Marshal(m)
			fmt.Fprintf(w, "data: %s\n\n", data)
			flusher.Flush()
			return

		case chunk.Usage:
			data, _ = json.Marshal(map[string]any{
				"tokens": chunk.TokensUsed,
				"limit":  chunk.MaxTokens,
			})
			fmt.Fprintf(w, "data: %s\n\n", data)
			flusher.Flush()

		default:
			data, _ = json.Marshal(map[string]string{"text": chunk.Text})
			fmt.Fprintf(w, "data: %s\n\n", data)
			flusher.Flush()
		}
	}

	fmt.Fprintf(w, "data: [DONE]\n\n")
	flusher.Flush()
}
