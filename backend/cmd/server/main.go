package main

import (
	"flag"
	"ghostoperator/backend/internal/handler"
	"ghostoperator/backend/internal/llm"
	"log"
	"net/http"
	"os"
)

func main() {
	staticDir := flag.String("static", "../frontend", "directory to serve static files from")
	port := flag.String("port", "8080", "port to listen on")
	flag.Parse()

	apiKey := os.Getenv("GEMINI_API_KEY")
	if apiKey == "" {
		log.Fatal("GEMINI_API_KEY environment variable is not set")
	}

	llmClient := llm.NewClient(apiKey)

	replyHandler := handler.NewReplyHandler(llmClient)
	ocrHandler := handler.NewOCRHandler(llmClient)
	draftsHandler := handler.NewDraftsHandler(llmClient)

	mux := http.NewServeMux()
	mux.HandleFunc("/health", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("ok"))
	})
	mux.Handle("/reply", replyHandler)              // legacy endpoint (kept for compatibility)
	mux.Handle("/api/ocr", ocrHandler)              // POST image → parsed thread JSON
	mux.Handle("/api/draft-replies", draftsHandler) // POST thread → SSE candidates
	if *staticDir != "" {
		mux.Handle("/", http.FileServer(http.Dir(*staticDir)))
		log.Printf("Serving static files from: %s", *staticDir)
	}

	log.Printf("GhostOperator running on http://localhost:%s", *port)
	log.Fatal(http.ListenAndServe(":"+*port, mux))
}
