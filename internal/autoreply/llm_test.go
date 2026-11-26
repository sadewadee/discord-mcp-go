package autoreply

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

const testKey = "sk-test-secret-key"

func newClient(url string, timeout time.Duration) *OpenAI {
	return NewOpenAI(OpenAIConfig{BaseURL: url + "/v1/", APIKey: testKey, Model: "test-model", MaxTokens: 123,
		Timeout: timeout, RetryDelay: 10 * time.Millisecond})
}

func answer(w http.ResponseWriter, text string) {
	_ = json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"message": map[string]string{"role": "assistant", "content": text}}}})
}

func TestOpenAIRequest(t *testing.T) {
	var got map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" || r.Method != http.MethodPost {
			t.Errorf("request %s %s", r.Method, r.URL.Path)
		}
		if r.Header.Get("Authorization") != "Bearer "+testKey {
			t.Errorf("auth header %q", r.Header.Get("Authorization"))
		}
		_ = json.NewDecoder(r.Body).Decode(&got)
		answer(w, "  hello  ")
	}))
	defer srv.Close()
	text, err := newClient(srv.URL, time.Second).Complete(context.Background(), []Message{{"system", "S"}, {"user", "U"}})
	if err != nil || text != "hello" {
		t.Fatalf("Complete = %q, %v", text, err)
	}
	if got["model"] != "test-model" || got["max_tokens"].(float64) != 123 || got["stream"] != false {
		t.Errorf("body = %v", got)
	}
	if _, ok := got["tools"]; ok {
		t.Error("the LLM must get no tools")
	}
	msgs := got["messages"].([]any)
	if len(msgs) != 2 || msgs[1].(map[string]any)["content"] != "U" {
		t.Errorf("messages = %v", msgs)
	}
}

func TestOpenAIRetry(t *testing.T) {
	for _, tc := range []struct {
		name      string
		statuses  []int
		wantCalls int32
		wantErr   bool
	}{
		{"5xx then success", []int{502, 200}, 2, false},
		{"429 then success", []int{429, 200}, 2, false},
		{"5xx twice", []int{500, 503}, 2, true},
		{"4xx is not retried", []int{400}, 1, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var calls atomic.Int32
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				n := calls.Add(1)
				status := tc.statuses[min(int(n)-1, len(tc.statuses)-1)]
				if status != 200 {
					w.WriteHeader(status)
					_, _ = w.Write([]byte(`{"error":{"message":"upstream busy"}}`))
					return
				}
				answer(w, "ok")
			}))
			defer srv.Close()
			_, err := newClient(srv.URL, time.Second).Complete(context.Background(), []Message{{"user", "q"}})
			if (err != nil) != tc.wantErr || calls.Load() != tc.wantCalls {
				t.Errorf("err %v, calls %d", err, calls.Load())
			}
			if err != nil && (strings.Contains(err.Error(), testKey) || !strings.Contains(err.Error(), "upstream busy")) {
				t.Errorf("error text %q", err)
			}
		})
	}
}

func TestOpenAITimeout(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-time.After(2 * time.Second):
		}
	}))
	defer srv.Close()
	start := time.Now()
	_, err := newClient(srv.URL, 100*time.Millisecond).Complete(context.Background(), []Message{{"user", "q"}})
	if err == nil || !strings.Contains(err.Error(), "timed out") {
		t.Errorf("err = %v", err)
	}
	if time.Since(start) > time.Second {
		t.Errorf("took %s", time.Since(start))
	}
}

func TestOpenAIEmptyAnswer(t *testing.T) {
	for name, body := range map[string]string{
		"blank content": `{"choices":[{"message":{"content":"   "}}]}`,
		"no choices":    `{"choices":[]}`,
	} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte(body)) }))
		_, err := newClient(srv.URL, time.Second).Complete(context.Background(), []Message{{"user", "q"}})
		srv.Close()
		if !errors.Is(err, ErrEmptyAnswer) {
			t.Errorf("%s: err = %v", name, err)
		}
	}
}
