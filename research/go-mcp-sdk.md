# Go MCP SDK Options

Research date: 2026-04-15

## Recommendation: Official Go SDK

Use `github.com/modelcontextprotocol/go-sdk` — it's the official Tier 1 SDK, maintained by Anthropic/MCP org in collaboration with Google.

## Official Go SDK

- **Repo:** github.com/modelcontextprotocol/go-sdk
- **Status:** Stable v1.0.0
- **Tier:** 1 (same as TypeScript and Python)
- **Maintained by:** Anthropic/MCP org + Google
- **Transport:** stdio, SSE, streamable HTTP
- **Features:**
  - Server and Client as first-class types
  - Tool registration via `AddTool()` with automatic input schema inference
  - Notification support via server methods
  - JSON-RPC layer from gopls (battle-tested)
  - Type-safe through generics
- **Custom notifications:** Supports arbitrary notification method names (needed for `notifications/claude/channel`)

## Alternative: mark3labs/mcp-go

- **Repo:** github.com/mark3labs/mcp-go
- **Status:** Active, widely used community package
- **Transport:** stdio, SSE, streamable HTTP
- **Pros:** Less boilerplate, faster to get started
- **Cons:** Not official, less long-term guarantee

## Decision

D008: Use official Go MCP SDK (`github.com/modelcontextprotocol/go-sdk`) for C3.

**Why:** Official backing, Tier 1 support, maximum compatibility with Claude Code, long-term stability. The custom `notifications/claude/channel` method is supported since the SDK allows arbitrary notification method names.
