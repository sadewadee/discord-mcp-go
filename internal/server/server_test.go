package server

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/sadewadee/discord-mcp-go/internal/tools"
)

func testTools() []tools.Tool {
	return []tools.Tool{{
		Name:        "echo",
		Description: "Echo the text argument",
		Params:      []tools.Param{{Name: "text", Description: "Text", Required: true}},
		Handler: func(_ context.Context, a tools.Args) (string, error) {
			if a.Get("text") == "" {
				return "", errNoText
			}
			return "you said " + a.Get("text"), nil
		},
	}}
}

type textErr string

func (e textErr) Error() string { return string(e) }

const errNoText = textErr("text cannot be null")

func TestHealth(t *testing.T) {
	srv := httptest.NewServer(Handler(New(testTools(), nil), nil))
	defer srv.Close()

	resp, err := http.Get(srv.URL + HealthPath)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 || string(body) != `{"status":"UP"}` {
		t.Errorf("health = %d %q", resp.StatusCode, body)
	}
}

func TestStreamableHTTP(t *testing.T) {
	srv := httptest.NewServer(Handler(New(testTools(), nil), nil))
	defer srv.Close()

	ctx := context.Background()
	client := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "0"}, nil)
	session, err := client.Connect(ctx, &mcp.StreamableClientTransport{Endpoint: srv.URL + MCPPath}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()

	if got := session.InitializeResult().ServerInfo.Name; got != Name {
		t.Errorf("server name = %q", got)
	}

	list, err := session.ListTools(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(list.Tools) != 1 || list.Tools[0].Name != "echo" {
		t.Fatalf("tools = %+v", list.Tools)
	}

	res, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "echo", Arguments: map[string]any{"text": "hi"}})
	if err != nil {
		t.Fatal(err)
	}
	if res.IsError || res.Content[0].(*mcp.TextContent).Text != "you said hi" {
		t.Errorf("result = %+v", res)
	}

	res, err = session.CallTool(ctx, &mcp.CallToolParams{Name: "echo", Arguments: map[string]any{}})
	if err != nil {
		t.Fatal(err)
	}
	if !res.IsError || res.Content[0].(*mcp.TextContent).Text != "text cannot be null" {
		t.Errorf("error result = %+v", res)
	}
}
