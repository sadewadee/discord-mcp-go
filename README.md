# discord-mcp-go

A [Model Context Protocol (MCP)](https://modelcontextprotocol.io/introduction) server for the Discord API, written in Go.
It lets AI assistants such as Claude manage a Discord server through a bot account: send and read messages, manage channels, roles, threads, forums, events, invites, webhooks and emojis, and moderate members.

- Single static binary, no runtime dependencies
- Two transports: **stdio** (default) and **streamable HTTP** at `/mcp`
- 84 tools, all with string parameters and formatted text results

## Transport selection

The server picks its transport automatically; there is nothing to configure:

- **stdio** when an MCP client starts it (stdin is a pipe), including `docker run -i`.
- **HTTP** on port **8085** otherwise: started from a terminal, as a service, or with `docker run -d` / Docker Compose. MCP is served at `/mcp` and `/health` returns `{"status":"UP"}`. HTTP requires authentication (see [Authentication](#authentication-http)); without it the server refuses to start.

`MCP_TRANSPORT=stdio` or `MCP_TRANSPORT=http` forces one if the automatic choice is wrong for your setup. The port is always 8085; map it to another host port with Docker (`-p 9000:8085`) if needed.

## Requirements

- A Discord bot token. Create an application in the [Discord Developer Portal](https://discord.com/developers/applications), add a bot, and copy its token.
- In **Bot → Privileged Gateway Intents**, enable **Server Members Intent**. The member lookup and search tools depend on it, and the server will not start without it.
- Invite the bot to your server with the permissions the tools you use need (for example Manage Channels, Manage Roles, Moderate Members).

## Configuration

| Variable | Required | Default | Meaning |
| --- | --- | --- | --- |
| `DISCORD_TOKEN` | yes | | Bot token, without surrounding quotes. |
| `DISCORD_GUILD_ID` | no | | Default server ID. Tools that take a `guildId` may then omit it. |
| `MCP_TRANSPORT` | no | `auto` | Leave unset. Set `stdio` or `http` only to override the automatic choice. |
| `LOG_FILE` | no | `./logs/discord-mcp-server.log` | Log file. In stdio mode nothing but MCP traffic is written to stdout. |
| `MCP_PUBLIC_URL` | for OAuth | | Public HTTPS address of the server, e.g. `https://mcp.example.com`. Setting it turns on OAuth. |
| `DISCORD_CLIENT_SECRET` | for OAuth | | OAuth2 client secret of the Discord application (Developer Portal → OAuth2). |
| `DISCORD_CLIENT_ID` | no | the bot's application | Set only if sign-in uses a different Discord application than the bot. |
| `MCP_ALLOWED_USERS` | for OAuth¹ | | Comma-separated Discord user IDs allowed to sign in. |
| `OAUTH_DATA_FILE` | no | `./data/oauth.json` | Where registered clients and tokens are stored (token hashes only). |
| `MCP_AUTH_TOKEN` | for HTTP² | | A fixed bearer token, at least 32 characters, accepted alongside or instead of OAuth. |
| `MCP_INSECURE_NO_AUTH` | no | `false` | `true` serves HTTP without authentication. Only for setups where something in front of the server already authenticates every request. |

¹ The owner of `DISCORD_GUILD_ID` may always sign in, so with `DISCORD_GUILD_ID` set the list is optional.
² HTTP needs `MCP_AUTH_TOKEN` or OAuth (`MCP_PUBLIC_URL` + `DISCORD_CLIENT_SECRET`); otherwise the server refuses to start. stdio needs neither.

## Authentication (HTTP)

stdio needs no authentication: the MCP client starts the process itself. The HTTP endpoint controls your bot, so protect it:

| Setting | What clients do |
| --- | --- |
| `MCP_PUBLIC_URL` + `DISCORD_CLIENT_SECRET` | **OAuth 2.1.** Clients discover the server's authorization endpoints, register themselves, and send you to sign in with Discord and approve the client. Works with claude.ai connectors, Claude Code, Cursor and other OAuth-capable MCP clients. |
| `MCP_AUTH_TOKEN` | Clients send `Authorization: Bearer <token>`. Can be combined with OAuth. |
| neither | **The server refuses to start** and explains what to set. `MCP_INSECURE_NO_AUTH=true` overrides this (with a warning) for setups where a proxy in front already authenticates every request. |

### Setting up OAuth

1. Put the server behind HTTPS, for example a reverse proxy (Caddy, nginx, Traefik) or a tunnel, and note its address, e.g. `https://mcp.example.com`. The server itself listens on plain HTTP port 8085.
2. In the [Discord Developer Portal](https://discord.com/developers/applications), open the bot's application → **OAuth2**:
   - copy the **Client Secret** into `DISCORD_CLIENT_SECRET`;
   - under **Redirects**, add `https://mcp.example.com/oauth/discord/callback`.
3. Set `MCP_PUBLIC_URL=https://mcp.example.com`, and either `DISCORD_GUILD_ID` (its owner may sign in) or `MCP_ALLOWED_USERS` with the Discord user IDs of everyone who may use the server (Discord → Settings → Advanced → Developer Mode, then right-click a user → Copy User ID).
4. Keep `/app/data` (or `OAUTH_DATA_FILE`) on persistent storage, so clients stay signed in across restarts. Docker Compose does this with the `discord-mcp-data` volume.
5. In your MCP client, add `https://mcp.example.com/mcp`. On first use the client opens a browser: sign in with Discord, check the client name, and click **Allow**.

How it works:

- The server is its own authorization server, following the MCP authorization spec: protected resource metadata (`/.well-known/oauth-protected-resource`), authorization server metadata (`/.well-known/oauth-authorization-server`), dynamic client registration (`/oauth/register`), the authorization code flow with mandatory PKCE (`/oauth/authorize`, `/oauth/token`) and token revocation (`/oauth/revoke`).
- Discord sign-in only asks for the `identify` scope: the server learns who you are, nothing else.
- Access tokens last 1 hour; refresh tokens 30 days and are rotated on every use. Presenting an old refresh token again more than 30 seconds after it was exchanged, or reusing an authorization code, revokes everything issued from that sign-in. A retry within 30 seconds is only refused.
- The consent page shows the client's self-chosen name (marked as unverified) and the full redirect address. Only allow sign-ins you started yourself.
- Sign-in state is kept in a signed cookie in your browser, not on the server; a sign-in must finish within 10 minutes in the same browser.
- Whether a user is allowed is checked again on every request: remove someone from `MCP_ALLOWED_USERS` and restart, and their tokens stop working.
- Tokens and client secrets are stored only as SHA-256 hashes.

## Auto-reply to mentions (optional)

The server can also answer people: when someone mentions the bot in a guild channel, thread or forum post, it replies there with an answer from an LLM, taking the recent conversation into account. It is off unless `AUTOREPLY_ENABLED=true`, and it does not change the MCP tools.

- **HTTP mode only.** In stdio mode every MCP client starts its own server process, so the listener would answer once per client; the server refuses to start with `AUTOREPLY_ENABLED=true` in stdio mode.
- **Guild mentions only.** Direct messages are ignored, as are `@everyone`, `@here`, role mentions, bots, webhooks and the bot itself. A reply to one of the bot's messages counts when the replier leaves the mention on. With `DISCORD_GUILD_ID` set, only that server is answered.
- **Text only.** The LLM gets the conversation and nothing else: no tools and no access to the MCP toolset. Channel text is untrusted input, so the worst a hostile message can cause is a bad text answer.
- **Pings nobody.** Answers are sent with allowed mentions that only notify the person replied to; `@everyone` or a role mention in an answer arrives as plain text.
- Any OpenAI-compatible endpoint works (`POST {base URL}/chat/completions`, non-streaming).
- Answers are Discord replies, split into several messages beyond 2000 characters. If the LLM fails, times out or answers nothing (after one retry for HTTP 429/5xx), the bot adds a ⚠️ reaction to the question and posts nothing.
- Logs hold channel and message IDs, durations and errors, never message content, the API key or the bot token.

| Variable | Default | Meaning |
| --- | --- | --- |
| `AUTOREPLY_ENABLED` | `false` | `true` turns the listener on. |
| `AUTOREPLY_LLM_BASE_URL` | | OpenAI-compatible base URL including `/v1`. Required when enabled. |
| `AUTOREPLY_LLM_API_KEY` | | Bearer key for that endpoint. Required when enabled. |
| `AUTOREPLY_LLM_MODEL` | | Model ID. Required when enabled. |
| `AUTOREPLY_SYSTEM_PROMPT` | a short generic prompt | System prompt text. |
| `AUTOREPLY_SYSTEM_PROMPT_FILE` | | File holding the system prompt; wins over `AUTOREPLY_SYSTEM_PROMPT`. |
| `AUTOREPLY_CHANNEL_IDS` | all channels | Comma-separated channel IDs to answer in. Threads and forum posts match through their parent channel. |
| `AUTOREPLY_CONTEXT_MESSAGES` | `15` | Earlier messages sent as context, 0 to 50. |
| `AUTOREPLY_MAX_TOKENS` | `800` | `max_tokens` for each answer, 1 to 32000. |
| `AUTOREPLY_TIMEOUT_SECONDS` | `60` | Deadline for one LLM call, 1 to 600. |
| `AUTOREPLY_USER_COOLDOWN_SECONDS` | `10` | Minimum gap between two answers to the same user, 0 to 3600. Mentions inside it are ignored. |
| `AUTOREPLY_MAX_CONCURRENT` | `4` | LLM calls in flight at once, 1 to 32. Further mentions wait up to 30 seconds for a slot. |

**Message Content Intent.** Discord sends the text of a message that mentions the bot even without the privileged Message Content Intent, so mentions are always answered. The earlier messages used as context are different: without the intent they arrive empty ([Discord docs](https://docs.discord.com/developers/events/gateway#message-content-intent)). Enable **Message Content Intent** in the Developer Portal (Bot → Privileged Gateway Intents) so answers can use the conversation; the server logs a warning when context messages arrive empty. The server does not request the intent itself, so deployments without it keep working.

## Running

### Container image

Images for `linux/amd64` and `linux/arm64` are published to GitHub Container Registry:

| Tag | Built from |
| --- | --- |
| `ghcr.io/sadewadee/discord-mcp-go:latest` | the latest release tag |
| `ghcr.io/sadewadee/discord-mcp-go:1.2.3`, `:1.2` | release `v1.2.3` |
| `ghcr.io/sadewadee/discord-mcp-go:edge` | the latest commit on `main` |

HTTP:

```bash
export MCP_AUTH_TOKEN=$(openssl rand -hex 32)   # or set up OAuth, see Authentication
docker run -d --name discord-mcp --restart unless-stopped -p 8085:8085 \
  -e DISCORD_TOKEN -e DISCORD_GUILD_ID -e MCP_AUTH_TOKEN \
  -v discord-mcp-data:/app/data \
  ghcr.io/sadewadee/discord-mcp-go:latest
```

stdio (the MCP client starts the container):

```bash
docker run --rm -i -e DISCORD_TOKEN -e DISCORD_GUILD_ID ghcr.io/sadewadee/discord-mcp-go:latest
```

The MCP endpoint is `http://localhost:8085/mcp`, and `http://localhost:8085/health` returns `{"status":"UP"}`.

### Docker Compose (HTTP)

```bash
cp .env.example .env   # fill in DISCORD_TOKEN, and MCP_AUTH_TOKEN or the OAuth settings
```

```bash
docker compose up -d
```

Use `docker compose up -d --build` to build the image from source instead of pulling it.

### Installing into another container image

The binary is static, so it runs in any Linux image that has CA certificates. Copy it from the published image:

```dockerfile
FROM debian:bookworm-slim
RUN apt-get update && apt-get install -y --no-install-recommends ca-certificates && rm -rf /var/lib/apt/lists/*
COPY --from=ghcr.io/sadewadee/discord-mcp-go:latest /usr/local/bin/discord-mcp /usr/local/bin/discord-mcp
```

Or download a release archive (replace the version and architecture as needed):

```bash
curl -fsSL https://github.com/sadewadee/discord-mcp-go/releases/download/v0.1.0/discord-mcp_v0.1.0_linux_amd64.tar.gz \
  | tar -xz --strip-components=1 -C /usr/local/bin discord-mcp_v0.1.0_linux_amd64/discord-mcp
```

Check the installed version with `discord-mcp --version`.

### From source

Requires Go 1.25 or newer.

```bash
go install github.com/sadewadee/discord-mcp-go/cmd/discord-mcp@latest
```

Started by an MCP client, it speaks stdio. Run from a terminal, it serves HTTP on port 8085, which needs authentication:

```bash
DISCORD_TOKEN=... MCP_AUTH_TOKEN=$(openssl rand -hex 32) discord-mcp
```

## Client configuration

### Claude Code

HTTP with OAuth (Claude Code opens the browser to sign in):

```bash
claude mcp add discord-mcp --transport http https://mcp.example.com/mcp
```

HTTP with `MCP_AUTH_TOKEN`:

```bash
claude mcp add discord-mcp --transport http https://mcp.example.com/mcp --header "Authorization: Bearer YOUR_MCP_AUTH_TOKEN"
```

stdio:

```bash
claude mcp add discord-mcp -e DISCORD_TOKEN=YOUR_TOKEN -e DISCORD_GUILD_ID=OPTIONAL_SERVER_ID -- discord-mcp
```

### JSON-configured clients (Claude Desktop, Cursor, and others)

stdio with the binary on your `PATH`:

```json
{
  "mcpServers": {
    "discord-mcp": {
      "command": "discord-mcp",
      "env": {
        "DISCORD_TOKEN": "YOUR_DISCORD_BOT_TOKEN",
        "DISCORD_GUILD_ID": "OPTIONAL_DEFAULT_SERVER_ID"
      }
    }
  }
}
```

stdio through Docker:

```json
{
  "mcpServers": {
    "discord-mcp": {
      "command": "docker",
      "args": ["run", "--rm", "-i", "-e", "DISCORD_TOKEN", "-e", "DISCORD_GUILD_ID", "ghcr.io/sadewadee/discord-mcp-go:latest"],
      "env": {
        "DISCORD_TOKEN": "YOUR_DISCORD_BOT_TOKEN",
        "DISCORD_GUILD_ID": "OPTIONAL_DEFAULT_SERVER_ID"
      }
    }
  }
}
```

HTTP (clients that support OAuth sign in on first use):

```json
{
  "mcpServers": {
    "discord-mcp": {
      "url": "https://mcp.example.com/mcp"
    }
  }
}
```

HTTP with `MCP_AUTH_TOKEN`:

```json
{
  "mcpServers": {
    "discord-mcp": {
      "url": "https://mcp.example.com/mcp",
      "headers": { "Authorization": "Bearer YOUR_MCP_AUTH_TOKEN" }
    }
  }
}
```

### claude.ai

With OAuth set up, add a custom connector with the URL `https://mcp.example.com/mcp`. claude.ai registers itself and sends you to sign in with Discord.

## Troubleshooting

Startup failures are printed to stderr, which MCP clients show in their logs:

- **Discord rejected DISCORD_TOKEN**: the token is wrong, reset, or wrapped in quotes. Reset it in the Developer Portal and set it without quotes (an env file keeps quotes literally).
- **A privileged gateway intent is not enabled**: enable **Server Members Intent** in the Developer Portal.

Sign-in problems:

- **Discord says the redirect URI is invalid**: add `<MCP_PUBLIC_URL>/oauth/discord/callback` exactly under OAuth2 → Redirects.
- **"This Discord account is not allowed"**: add the user ID to `MCP_ALLOWED_USERS`, or sign in as the owner of `DISCORD_GUILD_ID`.
- **"This sign-in link has expired or was opened in a different browser"**: start again from the MCP client; the whole sign-in must happen in one browser within 10 minutes.

## Tools

All parameters are strings; numeric values such as `count` are passed as text and range-checked by the tool.
`guildId` is optional everywhere when `DISCORD_GUILD_ID` is set.

### Messages

- `send_message`: Send a message to a specific channel. Optional `replyToMessageId` sends it as a reply to that message without pinging its author
- `edit_message`: Edit a message from a specific channel
- `delete_message`: Delete a message from a specific channel
- `read_messages`: Read message history from a specific channel, optionally paginated with before/after/around
- `search_messages`: Search the most recent messages of a channel or thread for text (case-insensitive). Scans up to maxMessages messages from newest to oldest (default 500, max 1000); it is not a server-side search
- `add_reaction`: Add a reaction (emoji) to a specific message
- `remove_reaction`: Remove a specified reaction (emoji) from a message
- `get_attachment`: Get attachment metadata (filename, size, content type, URLs) from a specific message. Returns info only, does not download files.
- `get_message`: Get a single message by ID, in the same format as read_messages. If the message is a reply, the message it replies to is included
- `get_message_replies`: Find replies to a message by scanning the messages posted after it (up to maxMessages, default 500, max 1000). Discord has no replies lookup, so replies outside the scanned window are not found
- `wait_for_messages`: Wait until a new message is posted in a channel, thread or forum (any of its posts), then return it and anything else that arrives within half a second, oldest first, in the `read_messages` format. `after` returns already existing newer messages at once (not for forum channels); `timeoutSeconds` 1–50, default 25. Messages sent through this bot are included, so agents posting through the same bot can wait for each other. On timeout the result is `No new messages (waited Ns)`, which is not an error

### Users and members

- `get_user_id_by_name`: Get a Discord user's ID by exact username in a guild for ping usage <@id>. To match by nickname or global name, use search_members.
- `get_member_by_id`: Get a Discord guild member by stable user ID, including username, global name, nickname, effective name, and roles.
- `search_members`: Search Discord guild members by ID, username, global name, nickname, or effective name. Returns candidates with stable user IDs for reconciliation.
- `send_private_message`: Send a private message to a specific user
- `edit_private_message`: Edit a private message from a specific user
- `delete_private_message`: Delete a private message from a specific user
- `read_private_messages`: Read private message history from a specific user, optionally paginated with before/after/around

### Channels

- `delete_channel`: Delete a channel
- `create_text_channel`: Create a new text channel
- `find_channel`: Find a channel type and ID using name and server ID
- `list_channels`: List of all channels
- `edit_text_channel`: Edit settings of a text channel (name, topic, nsfw, slowmode, category, position)
- `get_channel_info`: Get detailed information about a channel
- `move_channel`: Move a channel to another category and/or change its position

### Categories

- `create_category`: Create a new category for channels
- `delete_category`: Delete a category
- `edit_category`: Edit a category (rename or move position)
- `find_category`: Find a category ID using name and server ID
- `list_channels_in_category`: List of channels in a specific category

### Channel permissions

- `list_channel_permission_overwrites`: Returns all permission overwrites for a channel with role/member breakdown and allow/deny details
- `upsert_role_channel_permissions`: Creates or updates permission overwrite for a role on a channel
- `upsert_member_channel_permissions`: Creates or updates permission overwrite for a member on a channel
- `delete_channel_permission_overwrite`: Deletes a permission overwrite for a role or member from a channel

### Roles

- `list_roles`: Returns a list of all roles on the server along with their ID, names, colors, positions, and permissions
- `create_role`: Creates a new role on the server with specified parameters
- `edit_role`: Updates settings of an existing role. All parameters except guild_id and role_id are optional
- `delete_role`: Permanently deletes a role from the server
- `assign_role`: Assigns a specified role to a user
- `remove_role`: Removes a specified role from a user

### Moderation

- `kick_member`: Kicks a member from the server. The user can rejoin with a new invite.
- `ban_member`: Bans a user from the server, preventing them from rejoining. Optionally deletes their recent messages.
- `unban_member`: Removes a ban from a user, allowing them to rejoin the server.
- `timeout_member`: Disables communication for a member for a specified duration (Timeout/Mute). Max 28 days.
- `remove_timeout`: Removes a timeout (unmute) from a member before it expires.
- `set_nickname`: Changes a member's nickname on the server. Pass empty or null nick to reset to original username.
- `get_bans`: Returns a list of banned users on the server with ban reasons.

### Voice and stage channels

- `create_voice_channel`: Create a new voice channel in a guild
- `create_stage_channel`: Create a new stage channel for audio events in a guild
- `edit_voice_channel`: Edit settings of a voice or stage channel (name, bitrate, user limit, region)
- `move_member`: Move a member to another voice channel. The member must already be connected to a voice channel.
- `disconnect_member`: Disconnect a member from their current voice channel
- `modify_voice_state`: Server mute or deafen a member across all voice channels. The member must be in a voice channel.

### Threads

- `list_threads`: List the threads of one channel (text, news, forum or media): active threads plus up to 100 recent archived public threads. A returned thread ID can be used as channelId in read_messages and send_message. Calling this also makes archived threads of the channel usable in get_thread_info and the message tools
- `get_thread_info`: Get detailed information about a thread: parent channel, owner, archived/locked state, creation time, message and member counts. Archived threads are found only after list_threads has been called on their parent channel
- `list_active_threads`: List all active threads in the server
- `create_thread`: Create a thread in a text or announcement channel, from a message (`messageId`) or standalone, optionally `private` (text channels only); `autoArchiveMinutes` 60, 1440 (default), 4320 or 10080. The returned ID works as `channelId` in the message tools. Forum channels use `create_forum_post`

### Forums

- `create_forum_channel`: Create a new forum channel
- `edit_forum_channel`: Edit settings of a forum channel (name, topic, nsfw, slowmode, category, position, default sort, default layout)
- `list_forum_channels`: List all forum channels in the server
- `get_forum_channel_info`: Get detailed information about a forum channel including tags and settings
- `list_forum_tags`: List all available tags in a forum channel
- `create_forum_post`: Create a new forum post (thread) with an initial message in a forum channel
- `list_forum_posts`: List active posts (threads) in a forum channel
- `modify_forum_post`: Modify a forum post: lock/unlock, archive/unarchive, pin/unpin, or change applied tags

### Scheduled events

- `create_guild_scheduled_event`: Schedule a new event on the server (voice, stage, or external)
- `edit_guild_scheduled_event`: Modify details of an existing event or change its status (start, complete, cancel)
- `delete_guild_scheduled_event`: Permanently delete a scheduled event
- `list_guild_scheduled_events`: List all active and scheduled events on the server
- `get_guild_scheduled_event_users`: Get list of users interested in a scheduled event

### Invites

- `create_invite`: Create a new invite link for a specific channel
- `list_invites`: List all active invites on the server with their statistics
- `delete_invite`: Delete (revoke) an invite so the link stops working
- `get_invite_details`: Get details about a specific invite (works for any public invite)

### Webhooks

- `create_webhook`: Create a new webhook on a specific channel
- `delete_webhook`: Delete a webhook
- `list_webhooks`: List of webhooks on a specific channel
- `send_webhook_message`: Send a message via webhook

### Emojis

- `list_emojis`: List all custom emojis on the server
- `get_emoji_details`: Get detailed information about a specific custom emoji
- `create_emoji`: Upload a new custom emoji to the server. Provide image as base64 (Data URI or raw) OR a direct image URL. Max 256KB
- `edit_emoji`: Edit an existing emoji's name or role restrictions
- `delete_emoji`: Permanently delete a custom emoji from the server

### Server

- `get_server_info`: Get detailed discord server information

## Claude Code skill: discord-listen

An agent session that has finished its turn is idle. It never calls `wait_for_messages` on its own, so messages addressed to it are not seen until someone starts it again (see #3).

The skill in [`skills/discord-listen`](skills/discord-listen) closes that gap on the client side. The session starts a small Python script as a background command and ends its turn. The script calls `wait_for_messages` over HTTP in a loop, without a language model. When a message for this session arrives, the script prints it and exits, and Claude Code re-invokes the session, which reads the message, replies through the normal Discord tools and starts the script again. Waiting costs no model tokens.

Install it as a personal skill, or into one project:

```bash
cp -r skills/discord-listen ~/.claude/skills/              # personal skill
cp -r skills/discord-listen <project>/.claude/skills/      # one project only
```

Configure the MCP endpoint and the credential with environment variables, or in a config file. The environment wins per field.

| Variable | Meaning |
| --- | --- |
| `DISCORD_MCP_URL` | Full URL of the `/mcp` endpoint, for example `https://mcp.example.com/mcp`. |
| `DISCORD_MCP_AUTH` | Complete `Authorization` header value, for example `Bearer <token>`. |
| `DISCORD_MCP_TOOL_PREFIX` | Tool name prefix when the server sits behind an MCP gateway, for example `discord__`. Default empty. |

The same keys can go into `${XDG_CONFIG_HOME:-~/.config}/discord-listen/config.json`:

```json
{
  "url": "https://mcp.example.com/mcp",
  "authorization": "Bearer <token>",
  "tool_prefix": ""
}
```

The file holds a credential. Restrict it to its owner with `chmod 600 ~/.config/discord-listen/config.json`. The script prints a warning if the file is readable by other users, and it never prints the authorization value.

Use it in a session with `/discord-listen`, giving the channel or thread ID, the agent name and the Discord user IDs whose messages count as instructions. You can also ask the agent in plain words, for example "listen on this thread for replies from user 123456789012345678". The agent starts the script in the background and ends its turn. Each run stores a cursor in `${XDG_STATE_HOME:-~/.local/state}/discord-listen`, so messages that arrive while the session works are returned by the next run.

`scripts/discord_listen.py` exits with these codes:

| Code | Meaning |
| --- | --- |
| `0` | Relevant messages were printed on stdout. |
| `2` | Configuration error, or the server reported a tool error (wrong channel ID, wrong prefix, missing permission). |
| `3` | The endpoint rejected the authentication (HTTP 401 or 403). |
| `4` | Too many consecutive network, server or decoding errors (`--max-errors`, default 20). |
| `10` | No relevant message within `--max-idle-minutes`. |
| `130` | Interrupted by SIGINT or SIGTERM. |

Limits:

- The session must stay open. A listener cannot wake a session that has been closed.
- Each listened channel needs one background process. Stop the old process before starting a new one for the same channel.
- Latency is that of `wait_for_messages`: a message is delivered as soon as the server sees it, and the script's next call returns it.
- It needs a Claude Code version that re-invokes the session when a background command exits.

## Development

```bash
go build ./...
go vet ./...
go test ./...
python3 -m unittest discover -s skills/discord-listen/scripts -p 'test_*.py'
```

Tests need no Discord connection. See [`CLAUDE.md`](CLAUDE.md) for the layout and the output contracts the tools keep.

## Releasing

Push a tag such as `v0.1.0`. The release workflow publishes the image tags above and attaches binaries for Linux, macOS and Windows (with `checksums.txt`) to the GitHub release.

## License

[MIT](LICENSE)
