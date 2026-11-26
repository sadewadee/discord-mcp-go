package autoreply

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// Message is one chat message sent to the LLM.
type Message struct {
	Role    string `json:"role"` // "system", "user" or "assistant"
	Content string `json:"content"`
}

// LLM turns a conversation into an answer. It gets text only: no tools.
type LLM interface {
	Complete(ctx context.Context, messages []Message) (string, error)
}

// ErrEmptyAnswer is returned when the model answers with no text.
var ErrEmptyAnswer = errors.New("LLM returned an empty answer")

// OpenAIConfig configures an OpenAI-compatible chat completions client.
type OpenAIConfig struct {
	BaseURL    string // including /v1
	APIKey     string
	Model      string
	MaxTokens  int
	Timeout    time.Duration // deadline for one attempt
	RetryDelay time.Duration // wait before the single retry on 429/5xx; default 2s
	HTTPClient *http.Client
}

// OpenAI calls POST {BaseURL}/chat/completions without streaming.
type OpenAI struct{ cfg OpenAIConfig }

// NewOpenAI returns a client for cfg.
func NewOpenAI(cfg OpenAIConfig) *OpenAI {
	if cfg.HTTPClient == nil {
		cfg.HTTPClient = &http.Client{}
	}
	if cfg.RetryDelay == 0 {
		cfg.RetryDelay = 2 * time.Second
	}
	cfg.BaseURL = strings.TrimRight(cfg.BaseURL, "/")
	return &OpenAI{cfg: cfg}
}

// statusError is a non-2xx answer from the LLM endpoint. Its text holds the
// status and the endpoint's error message, never the request.
type statusError struct {
	Status  int
	Message string
}

func (e *statusError) Error() string {
	if e.Message == "" {
		return fmt.Sprintf("LLM endpoint answered HTTP %d", e.Status)
	}
	return fmt.Sprintf("LLM endpoint answered HTTP %d: %s", e.Status, e.Message)
}

func retryable(err error) bool {
	var se *statusError
	return errors.As(err, &se) && (se.Status == http.StatusTooManyRequests || se.Status >= 500)
}

// Complete sends the conversation and returns the answer text. A 429 or 5xx
// answer is retried once after RetryDelay.
func (c *OpenAI) Complete(ctx context.Context, messages []Message) (string, error) {
	answer, err := c.attempt(ctx, messages)
	if err == nil || !retryable(err) {
		return answer, err
	}
	select {
	case <-ctx.Done():
		return "", err
	case <-time.After(c.cfg.RetryDelay):
	}
	return c.attempt(ctx, messages)
}

func (c *OpenAI) attempt(ctx context.Context, messages []Message) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, c.cfg.Timeout)
	defer cancel()
	body, err := json.Marshal(map[string]any{
		"model":      c.cfg.Model,
		"messages":   messages,
		"max_tokens": c.cfg.MaxTokens,
		"stream":     false,
	})
	if err != nil {
		return "", err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.cfg.BaseURL+"/chat/completions", bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Authorization", "Bearer "+c.cfg.APIKey)
	resp, err := c.cfg.HTTPClient.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return "", fmt.Errorf("LLM call timed out after %s", c.cfg.Timeout)
		}
		// The URL in a transport error holds no secret; the key is a header.
		return "", fmt.Errorf("LLM request failed: %w", err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return "", fmt.Errorf("reading LLM response: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return "", &statusError{Status: resp.StatusCode, Message: errorMessage(raw)}
	}
	var out struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return "", fmt.Errorf("LLM response is not valid JSON: %w", err)
	}
	if len(out.Choices) == 0 || strings.TrimSpace(out.Choices[0].Message.Content) == "" {
		return "", ErrEmptyAnswer
	}
	return strings.TrimSpace(out.Choices[0].Message.Content), nil
}

// errorMessage extracts error.message from an OpenAI-style error body,
// shortened for logs.
func errorMessage(raw []byte) string {
	var e struct {
		Error struct {
			Message string `json:"message"`
			Type    string `json:"type"`
		} `json:"error"`
	}
	if json.Unmarshal(raw, &e) != nil {
		return ""
	}
	msg := e.Error.Message
	if msg == "" {
		msg = e.Error.Type
	}
	if r := []rune(msg); len(r) > 200 {
		msg = string(r[:200]) + "…"
	}
	return msg
}
