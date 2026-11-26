# CLAUDE.md

## Project

`discord-mcp-go` is a Model Context Protocol (MCP) server that exposes the Discord API as MCP tools, so an AI client can manage a Discord server through a bot account.

## Stack

- Go 1.25
- [`github.com/bwmarrin/discordgo`](https://github.com/bwmarrin/discordgo) for the Discord gateway, state cache and REST API
- [`github.com/modelcontextprotocol/go-sdk`](https://github.com/modelcontextprotocol/go-sdk) for MCP (stdio and streamable HTTP)

## Layout

```
cmd/discord-mcp/main.go        entrypoint: config, logging, gateway, transport
internal/config/               environment variables
internal/gateway/              connects to Discord, waits for guilds and member chunking, startup error messages
internal/server/               MCP server, stdio and streamable HTTP transports, /health
internal/tools/
  registry.go                  Tool/Param types, argument parsing, error text, MCP registration
  lookup.go                    guild/channel/role/member lookups in the state cache, default guild
  format.go                    timestamp, date, file size and name formatting
  messages.go                  message line format, mention rendering, history paging helpers
  <group>.go                   one file per tool group (message, user, channel, role, ...)
```

Every tool group is a method on `*Toolset` that returns `[]Tool`; `Toolset.All` collects them.

## Build, test, run

```bash
go build ./...
go vet ./...
go test ./...
DISCORD_TOKEN=... MCP_TRANSPORT=http go run ./cmd/discord-mcp
```

Container builds and `docker compose up` run on a remote server, never on the development Mac.

## Configuration

| Variable | Required | Meaning |
| --- | --- | --- |
| `DISCORD_TOKEN` | yes | Bot token. The process exits with status 1 if it is empty or rejected. |
| `DISCORD_GUILD_ID` | no | Default guild. When set, tools may omit `guildId`. |
| `MCP_TRANSPORT` | no | `stdio` (default) or `http`. |
| `PORT` | no | HTTP port, default `8085`. |
| `LOG_FILE` | no | Log file, default `./logs/discord-mcp-server.log`. |

Runtime secrets live in `.env`, which is gitignored. Never commit a token.

## Contracts MCP clients depend on

- **Two transports.** stdio is the default. `MCP_TRANSPORT=http` serves streamable HTTP at `/mcp` on port 8085, with a health endpoint at `/health` that returns `{"status":"UP"}`. `docker-compose.yml` health-checks that endpoint.
- **stdout is the MCP channel in stdio mode.** Nothing else may be written to it. Logs go to `LOG_FILE` (and also stdout in HTTP mode); startup failures go to stderr, where MCP clients pick them up. discordgo's logger and the standard `log` package are routed to the same place.
- **Actionable startup errors.** A rejected token (close code 4004 or HTTP 401) or a missing privileged intent (close code 4014) produces a specific message with the fix (`gateway.StartupFailure`).
- **Gateway intents.** All non-privileged intents plus `GUILD_MEMBERS`. Members of every guild are chunked into the state cache at startup; member lookup and search tools rely on that cache.
- **Tool names and parameters.** Tool names are `snake_case`; parameter names are `camelCase` (`channelId`, `guildId`, `messageId`). All parameters are strings in the schema, including numeric ones such as `count`, which are parsed and range-checked inside the tool. Numbers and booleans sent by a client are accepted and converted to text.
- **Default guild.** Tools resolve `guildId` through `Toolset.guild`: an empty argument falls back to `DISCORD_GUILD_ID`, and a tool fails with a clear error if neither is set.
- **Results are formatted text**, not JSON. Clients and prompts may depend on the exact wording and Markdown shape, for example the message line format in `Toolset.formatMessage`. Timestamps use `formatTimestamp` (`2024-05-01T12:34:56.789Z`).
- **Errors are plain messages** such as `Channel not found by channelId`, returned as tool errors (`isError: true`). Discord API errors are shown as `<code>: <message>`, for example `10008: Unknown Message`.
- **Scan-based tools state their coverage.** `search_messages` and `get_message_replies` page through history 100 messages at a time, up to a caller-set limit (default 500, maximum 1000), and report whether the whole history was covered.
- **Message tools accept text, announcement and thread channel IDs** (`Toolset.messageChannel`).

When one of these changes, update this section in the same change.

## Conventions

- Cache first: guilds, channels, roles and members are read from `s.State`. Archived threads and members missing from the cache need an explicit REST call; handle the miss rather than assuming the entity is cached.
- Validation errors are returned with `errors.New` using the exact message text; Discord API errors are returned unchanged so `ErrorText` can format them.
- Keep helpers pure where possible and cover them with table tests. Tests must not need a Discord connection; `newTestToolset` in `format_message_test.go` builds a Toolset over an in-memory state cache.
