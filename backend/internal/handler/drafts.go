package handler

import (
	"encoding/json"
	"fmt"
	"ghostoperator/backend/internal/llm"
	"net/http"
)

// Drafter generates multi-tone reply candidates from a parsed thread.
type Drafter interface {
	DraftReplies(thread []llm.ThreadMessage, opts llm.DraftOptions, out chan<- llm.DraftChunk)
}

// DraftsHandler handles POST /api/draft-replies — streams SSE candidate chunks.
type DraftsHandler struct {
	llm Drafter
}

func NewDraftsHandler(llmClient Drafter) *DraftsHandler {
	return &DraftsHandler{llm: llmClient}
}

func (h *DraftsHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
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

	var req struct {
		Thread  []llm.ThreadMessage `json:"thread"`
		Tones   []string            `json:"tones"`
		Length  string              `json:"length"`
		Context string              `json:"context"`
		Model   string              `json:"model"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid JSON body", http.StatusBadRequest)
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Access-Control-Allow-Origin", "*")

	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming not supported", http.StatusInternalServerError)
		return
	}

	opts := llm.DraftOptions{
		Model:   req.Model,
		Tones:   req.Tones,
		Length:  req.Length,
		Context: req.Context,
	}

	chunks := make(chan llm.DraftChunk, 64)
	go h.llm.DraftReplies(req.Thread, opts, chunks)

	for chunk := range chunks {
		var data []byte
		if chunk.Err != nil {
			m := map[string]any{"error": chunk.Err.Error()}
			if chunk.ErrCode != "" {
				m["errCode"] = chunk.ErrCode
				m["id"] = chunk.ID
			}
			data, _ = json.Marshal(m)
		} else {
			data, _ = json.Marshal(chunk)
		}
		fmt.Fprintf(w, "data: %s\n\n", data)
		flusher.Flush()
	}

	fmt.Fprintf(w, "data: [DONE]\n\n")
	flusher.Flush()
}
