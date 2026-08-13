package stashbox

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stashapp/stash/pkg/models"
)

func TestStudioNameQueryPattern(t *testing.T) {
	tests := map[string]string{
		"bride4k":            "b%r%i%d%e%4%k",
		"Bride 4K":           "B%r%i%d%e%4%K",
		"Desperate-Amateurs": "D%e%s%p%e%r%a%t%e%A%m%a%t%e%u%r%s",
	}
	for input, want := range tests {
		if got := studioNameQueryPattern(input); got != want {
			t.Errorf("studioNameQueryPattern(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestFindStudioFallsBackToFlexibleNameQuery(t *testing.T) {
	var operations []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			OperationName string         `json:"operationName"`
			Variables     map[string]any `json:"variables"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Fatal(err)
		}
		operations = append(operations, request.OperationName)
		w.Header().Set("Content-Type", "application/json")
		switch request.OperationName {
		case "FindStudio":
			_, _ = w.Write([]byte(`{"data":{"findStudio":null}}`))
		case "QueryStudios":
			input := request.Variables["input"].(map[string]any)
			if got := input["names"]; got != "b%r%i%d%e%4%k" {
				t.Errorf("names query = %#v, want b%%r%%i%%d%%e%%4%%k", got)
			}
			_, _ = w.Write([]byte(`{"data":{"queryStudios":{"count":1,"studios":[{"id":"aef5eb33-bc3e-4315-bd25-7a94fd78bda0","name":"Bride 4K","aliases":[],"urls":[{"url":"https://bride4k.com/en/","type":"HOME"}],"parent":null,"images":[]}]}}}`))
		default:
			t.Errorf("unexpected operation %q", request.OperationName)
		}
	}))
	defer server.Close()

	client := NewClient(models.StashBox{Endpoint: server.URL})
	studio, err := client.FindStudio(context.Background(), "bride4k")
	if err != nil {
		t.Fatal(err)
	}
	if studio == nil || studio.Name != "Bride 4K" {
		t.Fatalf("studio = %#v", studio)
	}
	if len(operations) != 2 || operations[0] != "FindStudio" || operations[1] != "QueryStudios" {
		t.Fatalf("operations = %#v", operations)
	}
}
