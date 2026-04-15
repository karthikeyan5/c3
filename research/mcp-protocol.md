# MCP Stdio Protocol — What We Need to Implement

Research date: 2026-04-15
Source: Analysis of official Telegram plugin (0.0.6/server.ts)

## Protocol Basics

- JSON-RPC 2.0 over stdin/stdout
- Messages delimited by newlines (no embedded newlines)
- Server reads from stdin, writes to stdout, stderr for logging

## Server Initialization

Must declare these capabilities:
```json
{
  "name": "telegram",
  "version": "1.0.0",
  "capabilities": {
    "tools": {},
    "experimental": {
      "claude/channel": {},
      "claude/channel/permission": {}
    }
  },
  "instructions": ["...user-facing instructions for Claude..."]
}
```

## Channel Notifications (Inbound: Plugin -> Claude Code)

Method: `notifications/claude/channel`

```json
{
  "jsonrpc": "2.0",
  "method": "notifications/claude/channel",
  "params": {
    "content": "text message from user",
    "meta": {
      "chat_id": "string",
      "message_id": "string",
      "user": "string (username or numeric ID)",
      "user_id": "string",
      "ts": "ISO8601 timestamp",
      "image_path": "string (optional, absolute path)",
      "attachment_kind": "string (optional: document|voice|audio|video|video_note|sticker)",
      "attachment_file_id": "string (optional)",
      "attachment_size": "string (optional, bytes as string)",
      "attachment_mime": "string (optional)",
      "attachment_name": "string (optional, sanitized filename)"
    }
  }
}
```

No `id` field. No response expected.

## Tool Definitions (4 tools)

### reply
```json
{
  "name": "reply",
  "inputSchema": {
    "type": "object",
    "properties": {
      "chat_id": { "type": "string" },
      "text": { "type": "string" },
      "reply_to": { "type": "string", "description": "Message ID to thread under" },
      "files": { "type": "array", "items": { "type": "string" }, "description": "Absolute file paths" },
      "format": { "type": "string", "enum": ["text", "markdownv2"] }
    },
    "required": ["chat_id", "text"]
  }
}
```
Returns: `"sent (id: 12345)"`

### react
```json
{
  "name": "react",
  "inputSchema": {
    "type": "object",
    "properties": {
      "chat_id": { "type": "string" },
      "message_id": { "type": "string" },
      "emoji": { "type": "string" }
    },
    "required": ["chat_id", "message_id", "emoji"]
  }
}
```
Returns: `"reacted"`

### download_attachment
```json
{
  "name": "download_attachment",
  "inputSchema": {
    "type": "object",
    "properties": {
      "file_id": { "type": "string" }
    },
    "required": ["file_id"]
  }
}
```
Returns: absolute file path

### edit_message
```json
{
  "name": "edit_message",
  "inputSchema": {
    "type": "object",
    "properties": {
      "chat_id": { "type": "string" },
      "message_id": { "type": "string" },
      "text": { "type": "string" },
      "format": { "type": "string", "enum": ["text", "markdownv2"] }
    },
    "required": ["chat_id", "message_id", "text"]
  }
}
```
Returns: `"edited (id: 12345)"`

## Tool Call Format (Claude Code -> Plugin)

```json
{
  "jsonrpc": "2.0",
  "id": "numeric-string",
  "method": "tools/call",
  "params": {
    "name": "reply",
    "arguments": { "chat_id": "...", "text": "..." }
  }
}
```

## Tool Response Format (Plugin -> Claude Code)

Success:
```json
{
  "jsonrpc": "2.0",
  "id": "matching-id",
  "result": {
    "content": [{ "type": "text", "text": "sent (id: 12345)" }]
  }
}
```

Error:
```json
{
  "jsonrpc": "2.0",
  "id": "matching-id",
  "result": {
    "content": [{ "type": "text", "text": "error message" }],
    "isError": true
  }
}
```

## Permission Flow

Claude Code -> Plugin (request):
```json
{
  "method": "notifications/claude/channel/permission_request",
  "params": {
    "request_id": "5-char code (a-km-z)",
    "tool_name": "string",
    "description": "string",
    "input_preview": "JSON string"
  }
}
```

Plugin -> Claude Code (response):
```json
{
  "method": "notifications/claude/channel/permission",
  "params": {
    "request_id": "matching code",
    "behavior": "allow" | "deny"
  }
}
```

## Implementation Notes

- chat_id and user_id always strings (even though numeric)
- ts must be ISO8601: `new Date(timestamp * 1000).toISOString()`
- Sanitize filenames: remove `<>[];\r\n`
- Messages > 4096 chars must be chunked (Telegram limit)
- Photo extensions (.jpg/.jpeg/.png/.gif/.webp) send as photos, others as documents
- 50MB max per file attachment
- Atomic file writes for state (write to temp, rename)
- PID file at `~/.claude/channels/telegram/bot.pid`
- Clean shutdown on stdin EOF, SIGTERM, SIGINT, SIGHUP
