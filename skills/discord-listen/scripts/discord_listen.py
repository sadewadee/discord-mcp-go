#!/usr/bin/env python3
"""Wait for Discord messages addressed to one agent session.

The script calls the wait_for_messages tool of discord-mcp-go over streamable
HTTP in a loop. It uses no language model while waiting. It exits as soon as a
relevant message arrives, printing the messages on stdout. See ../SKILL.md for
how an agent session runs it.

Standard library only. Python 3.9 or newer.
"""

import argparse
import http.client
import json
import os
import re
import signal
import sys
import time
import urllib.error
import urllib.request
from dataclasses import dataclass
from pathlib import Path
from typing import Optional

PREFIX = "discord-listen: "
PROTOCOL_VERSION = "2025-03-26"
CLIENT_NAME = "discord-listen"
DEFAULT_TIMEOUT = 50
HTTP_SLACK_SECONDS = 20
REQUEST_TIMEOUT = 30
INITIAL_BACKOFF = 2
MAX_BACKOFF = 60
RESULT_PREVIEW = 200

EXIT_OK = 0
EXIT_CONFIG = 2
EXIT_AUTH = 3
EXIT_TOO_MANY_ERRORS = 4
EXIT_IDLE = 10
EXIT_INTERRUPTED = 130

ENV_URL = "DISCORD_MCP_URL"
ENV_AUTH = "DISCORD_MCP_AUTH"
ENV_PREFIX = "DISCORD_MCP_TOOL_PREFIX"

HEADER_RE = re.compile(r"^FROM (\S+) TO (\S+) TYPE (\S+)\s*$")
MESSAGE_START_RE = re.compile(
    r"^- \(ID: (\d+)\) \*\*\[(.*?)\]\*\* \(Author ID: (\d+)\) `([^`]*)`"
    r"(?: \[Reply to: \d+\])?: ```(.*)$"
)
SNOWFLAKE_RE = re.compile(r"^[0-9]+$")
SESSION_PHRASE = "Invalid session ID"


# ---------------------------------------------------------------------------
# Errors
# ---------------------------------------------------------------------------


class ListenerError(Exception):
    """Base class for failures that end the listener with an exit code."""


class ConfigError(ListenerError):
    """Missing or invalid configuration (exit 2)."""


class StateError(ListenerError):
    """Unreadable or invalid cursor state file (exit 2)."""


class Transient(ListenerError):
    """A failure worth retrying: network, timeout, 5xx, 429, unparseable body."""


class SessionInvalid(ListenerError):
    """The MCP session ID is no longer valid (HTTP 404 or 'Invalid session ID')."""


class AuthRejected(ListenerError):
    """HTTP 401 or 403 from the endpoint (exit 3, no retry)."""

    def __init__(self, status):
        super().__init__(f"HTTP {status}")
        self.status = status


class ToolFailure(ListenerError):
    """A tool error or JSON-RPC error. Retrying does not help (exit 2)."""


class Exhausted(ListenerError):
    """Too many consecutive failures (exit 4). The message is the last error."""


class DecodeError(ValueError):
    """A response body that cannot be decoded as a JSON-RPC response."""


# ---------------------------------------------------------------------------
# Data
# ---------------------------------------------------------------------------


@dataclass
class Header:
    sender: str
    recipient: str
    kind: str


@dataclass
class Message:
    id: str
    author_name: str
    author_id: str
    timestamp: str
    content: str
    header: Optional[Header] = None


@dataclass
class Config:
    url: str
    authorization: str
    tool_prefix: str


class HttpResponse:
    """One HTTP reply. Header names are stored lower-cased."""

    def __init__(self, status, headers, body):
        self.status = status
        self.headers = {key.lower(): value for key, value in headers.items()}
        self.body = body


# ---------------------------------------------------------------------------
# Result text parsing
# ---------------------------------------------------------------------------


def parse_header(content):
    """Return the agent header on the first line of content, or None."""
    first_line = content.split("\n", 1)[0]
    match = HEADER_RE.match(first_line)
    if match is None:
        return None
    return Header(match.group(1), match.group(2), match.group(3))


def _split_blocks(text):
    """Split a result into (start match, following lines) for each message line.

    Every line that matches MESSAGE_START_RE starts a block, including a line
    that message content may have forged. The lines after it belong to it until
    the next start line.
    """
    blocks = []
    for raw_line in text.split("\n"):
        line = raw_line.rstrip("\r")
        match = MESSAGE_START_RE.match(line)
        if match is not None:
            blocks.append((match, [match.group(5)]))
        elif blocks:
            blocks[-1][1].append(line)
    return blocks


def _complete_message(match, lines):
    """Build a Message from a block that has its closing fence, else None."""
    body = "\n".join(lines)
    close = body.rfind("```")
    if close < 0:
        return None
    content = body[:close]
    return Message(
        id=match.group(1),
        author_name=match.group(2),
        author_id=match.group(3),
        timestamp=match.group(4),
        content=content,
        header=parse_header(content),
    )


def _truncated_message(match, lines):
    """Build a Message from a block whose content may be cut short."""
    body = "\n".join(lines)
    stripped = body.rstrip()
    if stripped.endswith("```"):
        body = stripped[:-3]
    return Message(
        id=match.group(1),
        author_name=match.group(2),
        author_id=match.group(3),
        timestamp=match.group(4),
        content=body,
        header=parse_header(body),
    )


def parse_messages(text):
    """Return every message whose block has a closing fence.

    This does not check the result against the count the server declares.
    Input from the server goes through trusted_messages instead.
    """
    messages = []
    for match, lines in _split_blocks(text):
        message = _complete_message(match, lines)
        if message is not None:
            messages.append(message)
    return messages


COUNT_LINE_RE = re.compile(
    r"^\*\*(?:Received (\d+) new messages|Retrieved (\d+) messages):\*\*\s*$"
)


def declared_count(text):
    """Return the message count the server states on the first line, or None."""
    first_line = text.lstrip().split("\n", 1)[0].rstrip("\r")
    match = COUNT_LINE_RE.match(first_line)
    if match is None:
        return None
    return int(match.group(1) or match.group(2))


def trusted_messages(text):
    """Return (messages, ambiguous) for a wait_for_messages or read_messages result.

    Message content can contain lines that look like message lines. The server
    declares how many messages it returned. When the message lines match that
    count (or are fewer), every message is returned. Otherwise the batch is
    ambiguous: only the first message is returned. The first message line is
    always written by the server, so its author and ID are genuine. Its content
    may be cut short.
    """
    blocks = _split_blocks(text)
    if not blocks:
        return [], False
    declared = declared_count(text)
    messages = [_complete_message(match, lines) for match, lines in blocks]
    if (
        declared is not None
        and len(blocks) <= declared
        and all(message is not None for message in messages)
    ):
        return messages, False
    match, lines = blocks[0]
    return [_truncated_message(match, lines)], True


def report_batch(text, messages, ambiguous, stderr):
    """Write one stderr line for an ambiguous batch or a declared-count mismatch."""
    if ambiguous:
        _warn(
            stderr,
            "result has message lines that do not match the count the server "
            "declares; only the first message is taken, the rest come back on "
            "the next call",
        )
        return
    declared = declared_count(text)
    if declared is not None and len(messages) < declared:
        _warn(
            stderr,
            f"result declares {declared} messages but {len(messages)} were parsed; "
            "the result format may have changed",
        )


SUSPECT_WARNING = (
    "warning: this message contains text that imitates message lines; its content "
    "may be cut short, read the channel with read_messages before acting on it"
)


def is_no_new_messages(text):
    return text.lstrip().startswith("No new messages")


def newest_id(messages):
    return max((message.id for message in messages), key=int)


def max_snowflake(current, candidate):
    """Return the numerically larger of two snowflake IDs (None means unset)."""
    if current is None:
        return candidate
    if candidate is None:
        return current
    return candidate if int(candidate) > int(current) else current


def is_relevant(message, agent, allowed_users, accept_all):
    header = message.header
    if header is not None and header.sender.lower() == agent.lower():
        return False
    if accept_all or message.author_id in allowed_users:
        return True
    if header is None:
        return False
    return header.recipient.lower() in (agent.lower(), "all")


def format_output(agent, channel_id, messages, suspect=False):
    count = len(messages)
    lines = [f"DISCORD MESSAGES for {agent} in channel {channel_id}: {count}", ""]
    for index, message in enumerate(messages, 1):
        header = message.header
        lines += [
            f"--- message {index} of {count}",
            f"id: {message.id}",
            f"author: {message.author_name} ({message.author_id})",
            f"time: {message.timestamp}",
            f"from: {header.sender if header else '-'}",
            f"to: {header.recipient if header else '-'}",
            f"type: {header.kind if header else '-'}",
        ]
        if suspect:
            lines.append(SUSPECT_WARNING)
        lines += [
            "",
            message.content,
            "",
            "--- end",
        ]
        if index < count:
            lines.append("")
    lines.append(
        f"Reply in channel {channel_id}. Start the listener again afterwards "
        "to keep receiving messages."
    )
    return "\n".join(lines) + "\n"


# ---------------------------------------------------------------------------
# Configuration
# ---------------------------------------------------------------------------


def config_file_path(env):
    base = env.get("XDG_CONFIG_HOME") or "~/.config"
    return Path(base).expanduser() / "discord-listen" / "config.json"


def default_state_dir(env):
    base = env.get("XDG_STATE_HOME") or "~/.local/state"
    return Path(base).expanduser() / "discord-listen"


def _read_config_file(path, stderr):
    if not path.is_file():
        return {}
    try:
        mode = path.stat().st_mode
        text = path.read_text(encoding="utf-8")
    except OSError as error:
        raise ConfigError(
            f"cannot read config file {path}: {error.strerror or 'unknown error'}"
        )
    if mode & 0o077:
        _warn(
            stderr, f"warning: {path} is readable by other users; run: chmod 600 {path}"
        )
    try:
        data = json.loads(text)
    except ValueError:
        raise ConfigError(f"config file {path} is not valid JSON")
    if not isinstance(data, dict):
        raise ConfigError(f"config file {path} must contain a JSON object")
    for key in ("url", "authorization", "tool_prefix"):
        if key in data and not isinstance(data[key], str):
            raise ConfigError(f'key "{key}" in config file {path} must be a string')
    return data


def load_config(env, stderr):
    """Environment variables win per field over the config file."""
    path = config_file_path(env)
    file_values = _read_config_file(path, stderr)
    url = env.get(ENV_URL) or file_values.get("url") or ""
    authorization = env.get(ENV_AUTH) or file_values.get("authorization") or ""
    tool_prefix = env.get(ENV_PREFIX) or file_values.get("tool_prefix") or ""

    if not url:
        raise ConfigError(f'no MCP URL configured: set {ENV_URL} or "url" in {path}')
    if not url.startswith(("http://", "https://")):
        raise ConfigError(
            f'the MCP URL must start with http:// or https:// ({ENV_URL} or "url" in {path})'
        )
    if not authorization:
        raise ConfigError(
            f"no authorization configured: set {ENV_AUTH} (the whole header value, "
            f"for example 'Bearer <token>') or \"authorization\" in {path}"
        )
    return Config(url=url, authorization=authorization, tool_prefix=tool_prefix)


# ---------------------------------------------------------------------------
# Cursor state
# ---------------------------------------------------------------------------


def _sanitize(name):
    return re.sub(r"[^A-Za-z0-9._-]", "_", name)


def state_file_path(state_dir, agent, channel_id):
    return Path(state_dir) / f"{_sanitize(agent)}__{_sanitize(channel_id)}.json"


def load_cursor(path):
    try:
        text = path.read_text(encoding="utf-8")
    except FileNotFoundError:
        return None
    except OSError as error:
        raise StateError(
            f"cannot read state file {path}: {error.strerror or 'unknown error'}"
        )
    try:
        data = json.loads(text)
    except ValueError:
        raise StateError(
            f"state file {path} is not valid JSON; delete it to start over"
        )
    after = data.get("after") if isinstance(data, dict) else None
    if not isinstance(after, str) or not SNOWFLAKE_RE.match(after):
        raise StateError(
            f'state file {path} has no valid "after" ID; delete it to start over'
        )
    return after


def save_cursor(path, after):
    """Write the cursor atomically. The directory is 0700, the file 0600."""
    directory = path.parent
    if not directory.exists():
        directory.mkdir(parents=True, mode=0o700)
        os.chmod(directory, 0o700)
    temp = path.with_name(path.name + ".tmp")
    fd = os.open(str(temp), os.O_WRONLY | os.O_CREAT | os.O_TRUNC, 0o600)
    with os.fdopen(fd, "w", encoding="utf-8") as handle:
        json.dump({"after": after}, handle)
    os.chmod(temp, 0o600)
    os.replace(temp, path)


# ---------------------------------------------------------------------------
# JSON-RPC over streamable HTTP
# ---------------------------------------------------------------------------


def default_post(url, headers, body, timeout):
    """POST one request. HTTP error statuses are returned, not raised."""
    request = urllib.request.Request(url, data=body, headers=headers, method="POST")
    try:
        with urllib.request.urlopen(request, timeout=timeout) as response:
            return HttpResponse(
                response.status, dict(response.headers.items()), _read_text(response)
            )
    except urllib.error.HTTPError as error:
        try:
            return HttpResponse(
                error.code, dict(error.headers.items()), _read_text(error)
            )
        finally:
            error.close()


def _read_text(stream):
    return stream.read().decode("utf-8", "replace")


def _sse_events(body):
    """Return the data payload of each server-sent event in body."""
    events = []
    data_lines = []
    for raw_line in body.splitlines():
        line = raw_line.rstrip("\r")
        if line == "":
            if data_lines:
                events.append("\n".join(data_lines))
                data_lines = []
            continue
        if line.startswith("data:"):
            value = line[5:]
            if value.startswith(" "):
                value = value[1:]
            data_lines.append(value)
    if data_lines:
        events.append("\n".join(data_lines))
    return events


def _parse_json_objects(text):
    try:
        value = json.loads(text)
    except ValueError:
        raise DecodeError("response is not JSON")
    return value if isinstance(value, list) else [value]


def decode_rpc_response(body, request_id):
    """Return the JSON-RPC response whose id matches, from plain JSON or SSE."""
    text = body.strip()
    if not text:
        raise DecodeError("empty response body")
    if text.startswith(("{", "[")):
        candidates = _parse_json_objects(text)
    else:
        candidates = []
        for event in _sse_events(body):
            try:
                candidates.extend(_parse_json_objects(event))
            except DecodeError:
                continue
    for item in candidates:
        if (
            isinstance(item, dict)
            and item.get("id") == request_id
            and ("result" in item or "error" in item)
        ):
            return item
    raise DecodeError("no JSON-RPC response with the request id")


def _decode_or_transient(body, request_id):
    try:
        return decode_rpc_response(body, request_id)
    except DecodeError as error:
        raise Transient(f"unparseable response: {error}")


def _rpc_error_message(reply):
    error = reply.get("error")
    if isinstance(error, dict) and isinstance(error.get("message"), str):
        return error["message"]
    return "JSON-RPC error without a message"


def _result_text(result):
    parts = []
    for item in result.get("content") or []:
        if (
            isinstance(item, dict)
            and item.get("type") == "text"
            and isinstance(item.get("text"), str)
        ):
            parts.append(item["text"])
    return "\n".join(parts)


def _describe(error):
    return str(error) or type(error).__name__


def _is_rpc_body(body):
    """True when the body starts like a JSON or SSE payload, not a bare text."""
    return body.strip().startswith(("{", "[", "data:", "event:"))


class McpClient:
    """Minimal MCP client: one session, plain request/response calls."""

    def __init__(self, url, authorization, post):
        self.url = url
        self._authorization = authorization
        self._post = post
        self.session_id = None
        self.ready = False
        self._last_id = 0

    def _next_id(self):
        self._last_id += 1
        return self._last_id

    def _send(self, payload, timeout):
        headers = {
            "Content-Type": "application/json",
            "Accept": "application/json, text/event-stream",
            "Authorization": self._authorization,
        }
        if self.session_id:
            headers["Mcp-Session-Id"] = self.session_id
        body = json.dumps(payload).encode("utf-8")
        try:
            response = self._post(self.url, headers, body, timeout)
        except (OSError, http.client.HTTPException) as error:
            raise Transient(f"network error: {_describe(error)}")
        status = response.status
        if status in (401, 403):
            raise AuthRejected(status)
        if status == 404:
            raise SessionInvalid("MCP session is no longer valid")
        if status >= 400 and SESSION_PHRASE in response.body:
            raise SessionInvalid("MCP session is no longer valid")
        if (
            status < 400
            and SESSION_PHRASE in response.body
            and not _is_rpc_body(response.body)
        ):
            # A 2xx body that is not JSON or SSE is the bare server phrase. A
            # JSON-RPC or SSE body may carry the phrase as message content.
            raise SessionInvalid("MCP session is no longer valid")
        if status == 429 or status >= 500:
            raise Transient(f"HTTP {status} from MCP endpoint")
        if status >= 400:
            raise ToolFailure(f"HTTP {status} from MCP endpoint")
        return response

    def initialize(self):
        """Open a new session. Raises Transient if the endpoint rejects it."""
        self.ready = False
        self.session_id = None
        request_id = self._next_id()
        payload = {
            "jsonrpc": "2.0",
            "id": request_id,
            "method": "initialize",
            "params": {
                "protocolVersion": PROTOCOL_VERSION,
                "capabilities": {},
                "clientInfo": {"name": CLIENT_NAME, "version": "1"},
            },
        }
        try:
            response = self._send(payload, REQUEST_TIMEOUT)
            reply = _decode_or_transient(response.body, request_id)
            if "error" in reply:
                raise ToolFailure(_rpc_error_message(reply))
            self.session_id = response.headers.get("mcp-session-id") or None
            self._send(
                {"jsonrpc": "2.0", "method": "notifications/initialized"},
                REQUEST_TIMEOUT,
            )
        except SessionInvalid as error:
            raise Transient(f"session rejected during initialize: {error}")
        self.ready = True

    def call_tool(self, name, arguments, timeout):
        request_id = self._next_id()
        payload = {
            "jsonrpc": "2.0",
            "id": request_id,
            "method": "tools/call",
            "params": {"name": name, "arguments": arguments},
        }
        response = self._send(payload, timeout)
        reply = _decode_or_transient(response.body, request_id)
        if "error" in reply:
            raise ToolFailure(_rpc_error_message(reply))
        result = reply.get("result")
        if not isinstance(result, dict):
            raise Transient("response has no result object")
        text = _result_text(result)
        if result.get("isError"):
            raise ToolFailure(text or "the tool reported an error without a message")
        return text


def _call(client, name, arguments, timeout, stderr):
    """Call one tool, opening a session first and reopening it once on expiry."""
    if not client.ready:
        client.initialize()
    reopened = False
    while True:
        try:
            return client.call_tool(name, arguments, timeout)
        except SessionInvalid:
            if reopened:
                raise Transient("MCP session rejected again after reopening it")
            reopened = True
            _warn(stderr, "MCP session expired; opening a new session")
            client.initialize()


# ---------------------------------------------------------------------------
# Retry policy
# ---------------------------------------------------------------------------


class Retrier:
    """Exponential backoff with a consecutive-failure limit."""

    def __init__(self, max_errors, sleep, stderr):
        self.max_errors = max_errors
        self._sleep = sleep
        self._stderr = stderr
        self.failures = 0
        self.delay = INITIAL_BACKOFF

    def ok(self):
        self.failures = 0
        self.delay = INITIAL_BACKOFF

    def fail(self, message):
        self.failures += 1
        if self.failures >= self.max_errors:
            raise Exhausted(message)
        _warn(self._stderr, f"{message}; retrying in {self.delay} s")
        self._sleep(self.delay)
        self.delay = min(self.delay * 2, MAX_BACKOFF)

    def run(self, call):
        while True:
            try:
                return call()
            except Transient as error:
                self.fail(str(error))


# ---------------------------------------------------------------------------
# Listener
# ---------------------------------------------------------------------------


def _warn(stderr, message):
    stderr.write(PREFIX + message + "\n")
    stderr.flush()


def _bootstrap_cursor(options, client, retrier, tool_prefix, stderr):
    """Return the newest existing message ID, so old history is not replayed."""
    text = retrier.run(
        lambda: _call(
            client,
            tool_prefix + "read_messages",
            {"channelId": options.channel, "count": "1"},
            REQUEST_TIMEOUT,
            stderr,
        )
    )
    retrier.ok()
    messages, ambiguous = trusted_messages(text)
    report_batch(text, messages, ambiguous, stderr)
    if not messages:
        return None
    return newest_id(messages)


def _idle_expired(options, started, now):
    return (
        options.max_idle_minutes > 0 and now - started >= options.max_idle_minutes * 60
    )


def _listen(options, config, client, state_path, sleep, clock, stdout, stderr):
    started = clock()
    retrier = Retrier(options.max_errors, sleep, stderr)
    allowed = set(options.allow_user or [])
    wait_tool = config.tool_prefix + "wait_for_messages"
    wait_timeout = options.timeout + HTTP_SLACK_SECONDS

    cursor = load_cursor(state_path)
    if cursor is None and not options.from_start:
        cursor = _bootstrap_cursor(options, client, retrier, config.tool_prefix, stderr)
        if cursor is not None:
            save_cursor(state_path, cursor)

    while True:
        arguments = {
            "channelId": options.channel,
            "timeoutSeconds": str(options.timeout),
        }
        if cursor is not None:
            arguments["after"] = cursor
        text = retrier.run(
            lambda: _call(client, wait_tool, arguments, wait_timeout, stderr)
        )

        if is_no_new_messages(text):
            retrier.ok()
        else:
            messages, ambiguous = trusted_messages(text)
            if not messages:
                retrier.fail(
                    f"unexpected result, treated as nothing new: {text[:RESULT_PREVIEW]}"
                )
                continue
            retrier.ok()
            report_batch(text, messages, ambiguous, stderr)
            # In an ambiguous batch only the first message is genuine, so the
            # cursor moves to it and no further.
            cursor = max_snowflake(cursor, newest_id(messages))
            relevant = [
                message
                for message in messages
                if is_relevant(message, options.agent, allowed, options.all)
            ]
            if relevant:
                stdout.write(
                    format_output(
                        options.agent, options.channel, relevant, suspect=ambiguous
                    )
                )
                stdout.flush()
                save_cursor(state_path, cursor)
                return EXIT_OK
            save_cursor(state_path, cursor)

        if _idle_expired(options, started, clock()):
            stdout.write(
                f"No relevant messages for {options.max_idle_minutes} minutes.\n"
            )
            stdout.flush()
            return EXIT_IDLE


def _run(options, env, post, sleep, clock, stdout, stderr):
    try:
        config = load_config(env, stderr)
        state_dir = options.state_dir or default_state_dir(env)
        state_path = state_file_path(state_dir, options.agent, options.channel)
        client = McpClient(config.url, config.authorization, post)
        return _listen(
            options, config, client, state_path, sleep, clock, stdout, stderr
        )
    except ConfigError as error:
        _warn(stderr, str(error))
        return EXIT_CONFIG
    except StateError as error:
        _warn(stderr, str(error))
        return EXIT_CONFIG
    except AuthRejected as error:
        if error.status == 403:
            _warn(
                stderr,
                "Authentication was rejected by the MCP endpoint (HTTP 403). Check DISCORD_MCP_AUTH.",
            )
        else:
            _warn(
                stderr,
                "Authentication was rejected by the MCP endpoint (HTTP 401). Check DISCORD_MCP_AUTH.",
            )
        return EXIT_AUTH
    except Exhausted as error:
        _warn(
            stderr,
            f"too many consecutive errors ({options.max_errors}); last error: {error}",
        )
        return EXIT_TOO_MANY_ERRORS
    except ToolFailure as error:
        _warn(stderr, f"tool error: {error}")
        return EXIT_CONFIG
    except OSError as error:
        _warn(stderr, f"cannot write state file: {error.strerror or error}")
        return EXIT_CONFIG


def _snowflake(value):
    if not SNOWFLAKE_RE.match(value):
        raise argparse.ArgumentTypeError("must be a numeric Discord ID")
    return value


def _non_empty(value):
    if not value.strip():
        raise argparse.ArgumentTypeError("must not be empty")
    return value


def _ranged(low, high):
    def check(value):
        try:
            number = int(value)
        except ValueError:
            raise argparse.ArgumentTypeError("must be an integer")
        if number < low or number > high:
            raise argparse.ArgumentTypeError(f"must be between {low} and {high}")
        return number

    return check


def build_parser():
    parser = argparse.ArgumentParser(
        prog="discord_listen.py",
        description=(
            "Wait until a relevant message arrives in a Discord channel, then print it "
            "and exit. Exit codes: 0 messages printed, 2 configuration or tool error, "
            "3 authentication rejected, 4 too many consecutive errors, 10 idle limit "
            "reached, 130 interrupted."
        ),
    )
    parser.add_argument(
        "--channel",
        required=True,
        type=_snowflake,
        metavar="<channelId>",
        help="channel or thread ID to listen on",
    )
    parser.add_argument(
        "--agent",
        required=True,
        type=_non_empty,
        metavar="<name>",
        help="this agent's name, as used in the FROM/TO header",
    )
    parser.add_argument(
        "--allow-user",
        action="append",
        type=_snowflake,
        metavar="<userId>",
        help="Discord user ID whose messages are relevant (repeatable)",
    )
    parser.add_argument(
        "--all",
        action="store_true",
        help="treat every message from another sender as relevant",
    )
    parser.add_argument(
        "--timeout",
        type=_ranged(1, 50),
        default=DEFAULT_TIMEOUT,
        metavar="<1-50>",
        help="seconds per wait_for_messages call (default 50)",
    )
    parser.add_argument(
        "--max-idle-minutes",
        type=_ranged(0, 10**9),
        default=0,
        metavar="<n>",
        help="exit 10 after n minutes without a relevant message (default 0 = never)",
    )
    parser.add_argument(
        "--max-errors",
        type=_ranged(1, 10**6),
        default=20,
        metavar="<n>",
        help="consecutive failures before exit 4 (default 20)",
    )
    parser.add_argument(
        "--state-dir",
        default=None,
        metavar="<path>",
        help="cursor directory (default ${XDG_STATE_HOME:-~/.local/state}/discord-listen)",
    )
    parser.add_argument(
        "--from-start",
        action="store_true",
        help="without a saved cursor, do not read existing history first",
    )
    return parser


def main(
    argv=None, env=None, post=None, sleep=None, clock=None, stdout=None, stderr=None
):
    env = os.environ if env is None else env
    post = default_post if post is None else post
    sleep = time.sleep if sleep is None else sleep
    clock = time.monotonic if clock is None else clock
    stdout = sys.stdout if stdout is None else stdout
    stderr = sys.stderr if stderr is None else stderr
    try:
        options = build_parser().parse_args(argv)
    except SystemExit as exit_request:
        return exit_request.code if isinstance(exit_request.code, int) else EXIT_CONFIG
    try:
        return _run(options, env, post, sleep, clock, stdout, stderr)
    except KeyboardInterrupt:
        _warn(stderr, "interrupted")
        return EXIT_INTERRUPTED


if __name__ == "__main__":

    def _terminate(signum, frame):
        raise KeyboardInterrupt

    signal.signal(signal.SIGTERM, _terminate)
    sys.exit(main())
