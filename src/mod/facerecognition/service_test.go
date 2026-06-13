package facerecognition

import (
	"encoding/json"
	"image"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// fakeEmbeddingService spins up an httptest server implementing the embedding
// service contract, returning a fixed-dimension embedding.
func fakeEmbeddingService(t *testing.T, dimension int, wantToken string) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/info", func(w http.ResponseWriter, r *http.Request) {
		if wantToken != "" && r.Header.Get("Authorization") != "Bearer "+wantToken {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		json.NewEncoder(w).Encode(serviceInfoResponse{Dimension: dimension, Model: "fake"})
	})
	mux.HandleFunc("/embed", func(w http.ResponseWriter, r *http.Request) {
		if wantToken != "" && r.Header.Get("Authorization") != "Bearer "+wantToken {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		if r.Header.Get("Content-Type") != "image/jpeg" {
			http.Error(w, "bad content type", http.StatusBadRequest)
			return
		}
		emb := make([]float32, dimension)
		for i := range emb {
			emb[i] = float32(i+1) / float32(dimension)
		}
		json.NewEncoder(w).Encode(serviceEmbedResponse{Embedding: emb})
	})
	return httptest.NewServer(mux)
}

func blankFace() image.Image { return image.NewNRGBA(image.Rect(0, 0, 64, 64)) }

func TestServiceEngineEmbed(t *testing.T) {
	server := fakeEmbeddingService(t, 512, "")
	defer server.Close()

	engine, err := newServiceEngine(server.URL, "")
	if err != nil {
		t.Fatalf("newServiceEngine failed: %v", err)
	}
	defer engine.Close()

	if engine.Dimension() != 512 {
		t.Errorf("Dimension() = %d, want 512", engine.Dimension())
	}

	embedding, err := engine.Embed(blankFace())
	if err != nil {
		t.Fatalf("Embed failed: %v", err)
	}
	if len(embedding) != 512 {
		t.Fatalf("embedding length = %d, want 512", len(embedding))
	}
	//Result must be L2-normalized
	var sum float64
	for _, x := range embedding {
		sum += float64(x) * float64(x)
	}
	if sum < 0.99 || sum > 1.01 {
		t.Errorf("embedding is not L2-normalized, length^2 = %f", sum)
	}
}

func TestServiceEngineTrailingSlashURL(t *testing.T) {
	server := fakeEmbeddingService(t, 128, "")
	defer server.Close()

	//A trailing slash on the base URL must not break /info or /embed.
	engine, err := newServiceEngine(server.URL+"/", "")
	if err != nil {
		t.Fatalf("newServiceEngine failed: %v", err)
	}
	defer engine.Close()
	if _, err := engine.Embed(blankFace()); err != nil {
		t.Errorf("Embed failed with trailing-slash URL: %v", err)
	}
}

func TestServiceEngineAuth(t *testing.T) {
	server := fakeEmbeddingService(t, 256, "s3cret")
	defer server.Close()

	//Wrong/empty token must fail at the /info validation step.
	if _, err := newServiceEngine(server.URL, ""); err == nil {
		t.Errorf("expected auth failure without token")
	}
	//Correct token works.
	engine, err := newServiceEngine(server.URL, "s3cret")
	if err != nil {
		t.Fatalf("newServiceEngine with token failed: %v", err)
	}
	engine.Close()
}

func TestServiceEngineValidation(t *testing.T) {
	tests := []struct {
		name string
		url  string
	}{
		{"empty url", ""},
		{"missing scheme", "localhost:8090"},
		{"unreachable host", "http://127.0.0.1:0"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := newServiceEngine(tc.url, ""); err == nil {
				t.Errorf("newServiceEngine(%q) succeeded, want error", tc.url)
			}
		})
	}
}

func TestServiceEngineErrorResponse(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/info", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(serviceInfoResponse{Dimension: 512})
	})
	mux.HandleFunc("/embed", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(serviceEmbedResponse{Error: "no face detected"})
	})
	server := httptest.NewServer(mux)
	defer server.Close()

	engine, err := newServiceEngine(server.URL, "")
	if err != nil {
		t.Fatalf("newServiceEngine failed: %v", err)
	}
	defer engine.Close()

	_, err = engine.Embed(blankFace())
	if err == nil || !strings.Contains(err.Error(), "no face detected") {
		t.Errorf("Embed error = %v, want 'no face detected'", err)
	}
}
