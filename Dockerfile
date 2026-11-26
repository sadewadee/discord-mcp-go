FROM docker.io/library/golang:1.25-alpine AS build

WORKDIR /src

COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/discord-mcp ./cmd/discord-mcp

FROM docker.io/library/alpine:3.22

# ca-certificates for TLS to Discord; wget (busybox) is used by the compose health check.
RUN apk add --no-cache ca-certificates \
    && addgroup -S app && adduser -S -G app app \
    && mkdir -p /app/logs && chown -R app:app /app

WORKDIR /app
COPY --from=build /out/discord-mcp /usr/local/bin/discord-mcp

ENV DISCORD_TOKEN="" \
    DISCORD_GUILD_ID="" \
    MCP_TRANSPORT="stdio" \
    PORT="8085" \
    LOG_FILE="/app/logs/discord-mcp-server.log"

# Run as an unprivileged user.
USER app

EXPOSE 8085

# No HEALTHCHECK here: stdio mode has no HTTP endpoint. docker-compose.yml defines one for HTTP mode.

ENTRYPOINT ["discord-mcp"]
