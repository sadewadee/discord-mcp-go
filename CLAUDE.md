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
internal/oauth/                OAuth 2.1 authorization server (Discord sign-in) and bearer token check for /mcp
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
DISCORD_TOKEN=... go run ./cmd/discord-mcp   # from a terminal: HTTP on :8085
```

Container builds and `docker compose up` run on a remote server, never on the development Mac.

## Release

- `.github/workflows/ci.yml` runs gofmt, vet and `go test -race` on every push and pull request.
- `.github/workflows/release.yml` publishes a multi-arch image (`linux/amd64`, `linux/arm64`) to `ghcr.io/sadewadee/discord-mcp-go`: `:edge` and `:sha-<commit>` from `main`; `:X.Y.Z`, `:X.Y` and `:latest` from a `vX.Y.Z` tag, which also attaches binaries to the GitHub release.
- The version is injected with `-ldflags "-X main.version=..."` and reported to MCP clients and by `discord-mcp --version`.

## Configuration

| Variable | Required | Meaning |
| --- | --- | --- |
| `DISCORD_TOKEN` | yes | Bot token. The process exits with status 1 if it is empty or rejected. |
| `DISCORD_GUILD_ID` | no | Default guild. When set, tools may omit `guildId`. |
| `MCP_TRANSPORT` | no | Unset or `auto` picks the transport from stdin (`config.DetectTransport`); `stdio` or `http` forces one. |
| `LOG_FILE` | no | Log file, default `./logs/discord-mcp-server.log`. |
| `MCP_PUBLIC_URL` | for OAuth | Public https origin; setting it enables OAuth. |
| `DISCORD_CLIENT_SECRET` | for OAuth | Discord OAuth2 client secret for "Sign in with Discord". |
| `DISCORD_CLIENT_ID` | no | Discord application for sign-in; defaults to the bot's application (from READY). |
| `MCP_ALLOWED_USERS` | for OAuth, unless `DISCORD_GUILD_ID` | Discord user IDs allowed to sign in; the owner of `DISCORD_GUILD_ID` is always allowed. |
| `OAUTH_DATA_FILE` | no | OAuth clients and token hashes, default `./data/oauth.json` (`/app/data/oauth.json` in the image). |
| `MCP_AUTH_TOKEN` | no | Fixed bearer token (min. 32 characters), with or without OAuth. |

Runtime secrets live in `.env`, which is gitignored. Never commit a token.

## Contracts MCP clients depend on

- **Two transports, chosen automatically.** A pipe (or file or socket) on stdin means an MCP client launched the server: stdio. A terminal, `/dev/null` or closed stdin (shell, service, `docker run -d`, compose) means HTTP: streamable HTTP at `/mcp` on the fixed port 8085, with a health endpoint at `/health` that returns `{"status":"UP"}`. `MCP_TRANSPORT=stdio|http` overrides the choice. `docker-compose.yml` health-checks `/health`.
- **HTTP authentication.** With `MCP_PUBLIC_URL`, `/mcp` requires a bearer token and answers 401 with `WWW-Authenticate: Bearer resource_metadata="<MCP_PUBLIC_URL>/.well-known/oauth-protected-resource/mcp"`. The server is its own authorization server (RFC 8414 metadata, RFC 7591 registration, authorization code + S256 PKCE, refresh rotation with reuse detection, RFC 7009 revocation, RFC 9207 `iss`); users sign in with Discord (`identify` scope) and must pass the allow check, which is repeated on every request. A consent page names the client before a code is issued. `MCP_AUTH_TOKEN` alone protects `/mcp` without OAuth. With neither, HTTP is open and a warning is logged. `/health` is always open. stdio never uses authentication.
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
