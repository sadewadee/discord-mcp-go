// Package tools implements the MCP tools that manage a Discord server.
//
// Every tool takes string parameters only (numeric values such as counts are
// parsed and range-checked inside the tool) and returns formatted text.
package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strconv"

	"github.com/bwmarrin/discordgo"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// Param describes one string parameter of a tool.
type Param struct {
	Name        string
	Description string
	Required    bool
}

// Handler runs a tool with its parsed arguments and returns the result text.
type Handler func(ctx context.Context, a Args) (string, error)

// Tool is one MCP tool.
type Tool struct {
	Name        string
	Description string
	Params      []Param
	Handler     Handler
}

// req and opt build required and optional parameters.
func req(name, description string) Param {
	return Param{Name: name, Description: description, Required: true}
}
func opt(name, description string) Param { return Param{Name: name, Description: description} }

// Toolset holds what every tool needs: the Discord session (REST client plus
// gateway state cache) and the default guild.
type Toolset struct {
	s              *discordgo.Session
	defaultGuildID string
	log            *slog.Logger
}

// New returns a Toolset backed by session. defaultGuildID may be empty.
func New(session *discordgo.Session, defaultGuildID string, log *slog.Logger) *Toolset {
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	return &Toolset{
		s:              session,
		defaultGuildID: defaultGuildID,
		log:            log,
	}
}

// All returns every tool, sorted by name.
func (t *Toolset) All() []Tool {
	var all []Tool
	for _, group := range [][]Tool{
		t.discordTools(),
		t.messageTools(),
		t.userTools(),
		t.channelTools(),
		t.categoryTools(),
		t.webhookTools(),
		t.threadTools(),
		t.roleTools(),
		t.moderationTools(),
		t.voiceChannelTools(),
		t.scheduledEventTools(),
		t.inviteTools(),
		t.channelPermissionTools(),
		t.emojiTools(),
		t.forumTools(),
	} {
		all = append(all, group...)
	}
	sort.Slice(all, func(i, j int) bool { return all[i].Name < all[j].Name })
	return all
}

// InputSchema returns the JSON schema of the tool's parameters.
func (tool Tool) InputSchema() map[string]any {
	props := map[string]any{}
	required := []string{}
	for _, p := range tool.Params {
		props[p.Name] = map[string]any{"type": "string", "description": p.Description}
		if p.Required {
			required = append(required, p.Name)
		}
	}
	return map[string]any{
		"type":                 "object",
		"properties":           props,
		"required":             required,
		"additionalProperties": false,
	}
}

// Register adds every tool in tools to server.
func Register(server *mcp.Server, tools []Tool, log *slog.Logger) {
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	for _, tool := range tools {
		server.AddTool(&mcp.Tool{
			Name:        tool.Name,
			Description: tool.Description,
			InputSchema: tool.InputSchema(),
		}, tool.mcpHandler(log))
	}
}

func (tool Tool) mcpHandler(log *slog.Logger) mcp.ToolHandler {
	return func(ctx context.Context, r *mcp.CallToolRequest) (res *mcp.CallToolResult, err error) {
		args, perr := ParseArgs(r.Params.Arguments)
		if perr != nil {
			return errorResult(perr), nil
		}
		text, herr := tool.Call(ctx, args)
		if herr != nil {
			log.Warn("tool failed", "tool", tool.Name, "error", herr)
			return errorResult(herr), nil
		}
		return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: text}}}, nil
	}
}

// Call runs the tool, turning a panic into an error so one bad call cannot
// take the server down.
func (tool Tool) Call(ctx context.Context, a Args) (text string, err error) {
	defer func() {
		if p := recover(); p != nil {
			err = fmt.Errorf("internal error in %s: %v", tool.Name, p)
		}
	}()
	return tool.Handler(ctx, a)
}

func errorResult(err error) *mcp.CallToolResult {
	return &mcp.CallToolResult{
		IsError: true,
		Content: []mcp.Content{&mcp.TextContent{Text: ErrorText(err)}},
	}
}

// ErrorText is the message shown to the client for err. Discord API errors are
// shown as "<code>: <message>", for example "10008: Unknown Message".
func ErrorText(err error) string {
	var rest *discordgo.RESTError
	if errors.As(err, &rest) {
		if rest.Message != nil && rest.Message.Message != "" {
			return fmt.Sprintf("%d: %s", rest.Message.Code, rest.Message.Message)
		}
		if rest.Response != nil {
			return "HTTP " + rest.Response.Status
		}
	}
	var rl *discordgo.RateLimitError
	if errors.As(err, &rl) {
		return rl.Error()
	}
	return err.Error()
}

// Args holds the arguments of one tool call. An argument that was sent as
// JSON null or not sent at all is absent; anything else is kept as a string.
type Args map[string]*string

// ParseArgs decodes the raw arguments of a tool call. Numbers and booleans
// are accepted and converted to their text form.
func ParseArgs(raw json.RawMessage) (Args, error) {
	args := Args{}
	if len(raw) == 0 || string(raw) == "null" {
		return args, nil
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, fmt.Errorf("arguments must be a JSON object: %w", err)
	}
	for k, v := range m {
		switch x := v.(type) {
		case nil:
			continue
		case string:
			s := x
			args[k] = &s
		case float64:
			s := strconv.FormatFloat(x, 'f', -1, 64)
			args[k] = &s
		case bool:
			s := strconv.FormatBool(x)
			args[k] = &s
		default:
			b, _ := json.Marshal(x)
			s := string(b)
			args[k] = &s
		}
	}
	return args, nil
}

// Get returns the argument, or "" when it is absent.
func (a Args) Get(name string) string {
	if v := a[name]; v != nil {
		return *v
	}
	return ""
}

// Has reports whether the argument was sent (it may still be empty or blank).
func (a Args) Has(name string) bool { return a[name] != nil }
