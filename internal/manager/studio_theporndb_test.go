package manager

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestThePornDBStudioClientFindStudio(t *testing.T) {
	var authorization string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		authorization = r.Header.Get("Authorization")
		if got := r.URL.Query().Get("q"); got != "bride4k" {
			t.Errorf("q = %q, want bride4k", got)
		}
		_, _ = w.Write([]byte(`{"data":[{"uuid":"site-id","name":"Bride 4k","short_name":"bride4k","url":"https://bride4k.com","logo":"https://cdn.example/logo.png","poster":"","parent":{"uuid":"parent-id","name":"VIP 4K","short_name":"vip4k","url":"https://vip4k.com"}}]}`))
	}))
	defer server.Close()

	client := &thePornDBStudioClient{
		baseURL:    server.URL,
		apiKey:     "secret",
		httpClient: server.Client(),
	}
	studio, err := client.FindStudio(context.Background(), "bride4k")
	if err != nil {
		t.Fatal(err)
	}
	if authorization != "Bearer secret" {
		t.Fatalf("Authorization = %q", authorization)
	}
	if studio == nil || studio.Name != "Bride 4k" || len(studio.URLs) != 1 {
		t.Fatalf("studio = %#v", studio)
	}
	if studio.Parent == nil || studio.Parent.Name != "VIP 4K" {
		t.Fatalf("parent = %#v", studio.Parent)
	}
}

func TestThePornDBStudioClientRejectsNonExactRelevantResult(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"data":[{"uuid":"wrong","name":"Bride Training","short_name":"bridetraining","url":"https://wrong.example"}]}`))
	}))
	defer server.Close()
	client := &thePornDBStudioClient{baseURL: server.URL, apiKey: "secret", httpClient: server.Client()}
	studio, err := client.FindStudio(context.Background(), "bride4k")
	if err != nil {
		t.Fatal(err)
	}
	if studio != nil {
		t.Fatalf("studio = %#v, want nil", studio)
	}
}
