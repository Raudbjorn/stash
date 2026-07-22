package api

import (
	"fmt"
	"io"
	"net/http"
	"net/url"

	"github.com/go-chi/chi/v5"
	"github.com/stashapp/stash/pkg/logger"
)

type ttsRoutes struct{}

func (rs ttsRoutes) Routes() chi.Router {
	r := chi.NewRouter()

	r.Get("/pronounce", rs.Pronounce)

	return r
}

// Pronounce proxies TTS requests to Google Translate API
// This solves CORS issues and works on mobile browsers
func (rs ttsRoutes) Pronounce(w http.ResponseWriter, r *http.Request) {
	// Get query parameters
	text := r.URL.Query().Get("text")
	lang := r.URL.Query().Get("lang")

	if text == "" {
		http.Error(w, "text parameter is required", http.StatusBadRequest)
		return
	}

	// Default language to English (US) if not specified
	if lang == "" || lang == "en" {
		lang = "en-US"
	}

	// Convert language codes
	// Frontend uses: en, zh, es, etc.
	// Google TTS uses: en, zh-CN, es, etc.
	if lang == "zh" {
		lang = "zh-CN"
	}

	// Build Google TTS URL
	ttsURL := fmt.Sprintf("https://translate.google.com/translate_tts?ie=UTF-8&tl=%s&client=tw-ob&q=%s",
		url.QueryEscape(lang),
		url.QueryEscape(text))

	// Create HTTP client with proper headers
	client := &http.Client{}
	req, err := http.NewRequestWithContext(r.Context(), "GET", ttsURL, nil)
	if err != nil {
		logger.Errorf("Failed to create TTS request: %v", err)
		http.Error(w, "Failed to create request", http.StatusInternalServerError)
		return
	}

	// Add headers to mimic browser request
	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/91.0.4472.124 Safari/537.36")
	req.Header.Set("Referer", "https://translate.google.com/")

	// Make the request
	resp, err := client.Do(req)
	if err != nil {
		logger.Errorf("Failed to fetch TTS audio: %v", err)
		http.Error(w, "Failed to fetch audio", http.StatusInternalServerError)
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		logger.Errorf("TTS API returned status %d", resp.StatusCode)
		http.Error(w, fmt.Sprintf("TTS API error: %d", resp.StatusCode), http.StatusBadGateway)
		return
	}

	// Set response headers
	w.Header().Set("Content-Type", "audio/mpeg")
	w.Header().Set("Cache-Control", "public, max-age=86400") // Cache for 24 hours
	w.Header().Set("Access-Control-Allow-Origin", "*")

	// Copy the audio data to response
	_, err = io.Copy(w, resp.Body)
	if err != nil {
		logger.Errorf("Failed to write TTS audio: %v", err)
		return
	}

	logger.Debugf("TTS pronunciation served: text=%s, lang=%s", text, lang)
}
