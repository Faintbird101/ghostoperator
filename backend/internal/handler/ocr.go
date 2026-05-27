package handler

import (
	"encoding/json"
	"ghostoperator/backend/internal/llm"
	"io"
	"net/http"
)

// Extractor parses a chat screenshot into a structured thread.
type Extractor interface {
	ExtractThread(imageData []byte, mediaType string, model string) ([]llm.ThreadMessage, error)
}

// OCRHandler handles POST /api/ocr — accepts a multipart image, returns parsed thread JSON.
type OCRHandler struct {
	llm Extractor
}

func NewOCRHandler(llmClient Extractor) *OCRHandler {
	return &OCRHandler{llm: llmClient}
}

func (h *OCRHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
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

	file, header, err := r.FormFile("image")
	if err != nil {
		http.Error(w, "missing 'image' field", http.StatusBadRequest)
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

	model := r.FormValue("model")

	thread, err := h.llm.ExtractThread(imgData, mediaType, model)
	if err != nil {
		http.Error(w, "OCR failed: "+err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Access-Control-Allow-Origin", "*")
	json.NewEncoder(w).Encode(map[string]any{"thread": thread})
}
