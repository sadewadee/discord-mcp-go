# discord-mcp-go

A [Model Context Protocol (MCP)](https://modelcontextprotocol.io/introduction) server for the Discord API, written in Go.
It lets AI assistants such as Claude manage a Discord server through a bot account: send and read messages, manage channels, roles, threads, forums, events, invites, webhooks and emojis, and moderate members.

- Single static binary, no runtime dependencies
- Two transports: **stdio** (default) and **streamable HTTP** at `/mcp`
- 82 tools, all with string parameters and formatted text results

## Requirements

- A Discord bot token. Create an application in the [Discord Developer Portal](https://discord.com/developers/applications), add a bot, and copy its token.
- In **Bot → Privileged Gateway Intents**, enable **Server Members Intent**. The member lookup and search tools depend on it, and the server will not start without it.
- Invite the bot to your server with the permissions the tools you use need (for example Manage Channels, Manage Roles, Moderate Members).

## Configuration

| Variable | Required | Default | Meaning |
| --- | --- | --- | --- |
| `DISCORD_TOKEN` | yes | | Bot token, without surrounding quotes. |
| `DISCORD_GUILD_ID` | no | | Default server ID. Tools that take a `guildId` may then omit it. |
| `MCP_TRANSPORT` | no | `stdio` | `stdio` or `http`. |
| `PORT` | no | `8085` | HTTP port when `MCP_TRANSPORT=http`. |
| `LOG_FILE` | no | `./logs/discord-mcp-server.log` | Log file. In stdio mode nothing but MCP traffic is written to stdout. |

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
docker run -d --name discord-mcp --restart unless-stopped -p 8085:8085 \
  -e MCP_TRANSPORT=http -e DISCORD_TOKEN -e DISCORD_GUILD_ID \
  ghcr.io/sadewadee/discord-mcp-go:latest
```

stdio (the MCP client starts the container):

```bash
docker run --rm -i -e DISCORD_TOKEN -e DISCORD_GUILD_ID ghcr.io/sadewadee/discord-mcp-go:latest
```

The MCP endpoint is `http://localhost:8085/mcp`, and `http://localhost:8085/health` returns `{"status":"UP"}`.

### Docker Compose (HTTP)

```bash
cp .env.example .env   # then fill in DISCORD_TOKEN and, optionally, DISCORD_GUILD_ID
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

```bash
DISCORD_TOKEN=... discord-mcp
```

```bash
DISCORD_TOKEN=... MCP_TRANSPORT=http discord-mcp
```

## Client configuration

### Claude Code

HTTP:

```bash
claude mcp add discord-mcp --transport http http://localhost:8085/mcp
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

HTTP:

```json
{
  "mcpServers": {
    "discord-mcp": {
      "url": "http://localhost:8085/mcp"
    }
  }
}
```

## Troubleshooting

Startup failures are printed to stderr, which MCP clients show in their logs:

- **Discord rejected DISCORD_TOKEN**: the token is wrong, reset, or wrapped in quotes. Reset it in the Developer Portal and set it without quotes (an env file keeps quotes literally).
- **A privileged gateway intent is not enabled**: enable **Server Members Intent** in the Developer Portal.

## Tools

All parameters are strings; numeric values such as `count` are passed as text and range-checked by the tool.
`guildId` is optional everywhere when `DISCORD_GUILD_ID` is set.

### Messages

- `send_message`: Send a message to a specific channel
- `edit_message`: Edit a message from a specific channel
- `delete_message`: Delete a message from a specific channel
- `read_messages`: Read message history from a specific channel, optionally paginated with before/after/around
- `search_messages`: Search the most recent messages of a channel or thread for text (case-insensitive). Scans up to maxMessages messages from newest to oldest (default 500, max 1000); it is not a server-side search
- `add_reaction`: Add a reaction (emoji) to a specific message
- `remove_reaction`: Remove a specified reaction (emoji) from a message
- `get_attachment`: Get attachment metadata (filename, size, content type, URLs) from a specific message. Returns info only, does not download files.
- `get_message`: Get a single message by ID, in the same format as read_messages. If the message is a reply, the message it replies to is included
- `get_message_replies`: Find replies to a message by scanning the messages posted after it (up to maxMessages, default 500, max 1000). Discord has no replies lookup, so replies outside the scanned window are not found

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

## Development

```bash
go build ./...
go vet ./...
go test ./...
```

Tests need no Discord connection. See [`CLAUDE.md`](CLAUDE.md) for the layout and the output contracts the tools keep.

## Releasing

Push a tag such as `v0.1.0`. The release workflow publishes the image tags above and attaches binaries for Linux, macOS and Windows (with `checksums.txt`) to the GitHub release.

## License

[MIT](LICENSE)
