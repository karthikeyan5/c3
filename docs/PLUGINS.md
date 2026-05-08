# Writing C3 Plugins

A C3 plugin extends the broker with capabilities that aren't tied to a specific channel or CLI. Speech-to-text is one. Photo OCR, custom translation, slash-command shortcuts, scheduled outbound messages — all natural plugin territory. This document is for plugin authors.

If you want to add a new transport (Slack, web chat, voice), you want a **channel**, not a plugin — see `CHANNELS.md`. If you want to bridge a new CLI to C3, you want an **adapter** — see `ADAPTERS.md`.

## What a plugin is

A plugin is a unit of code that subscribes to one or more **hooks** the broker exposes during normal operation. The plugin runs inside the broker process (compiled-in) or as a sibling subprocess (external) and gets called every time its hook fires. Plugins are stateless across calls unless they explicitly persist state.

There are two delivery models:

- **Compiled-in plugins (v1, today)** — Go packages under `internal/plugin/builtins/<name>/`, statically linked into the broker binary. Discovered at build time. The STT plugin is shipped this way.
- **External subprocess plugins (v1.x roadmap)** — separate executables registered via a manifest under `~/.config/c3/plugins/<name>/`. Spoken to over a stdio JSON-RPC protocol. Allows plugins in any language without recompiling the broker.

This guide covers compiled-in plugins. The subprocess-plugin protocol is documented at the end with a note that it's not implemented in v1.

## Hook points

The broker exposes five hooks. A plugin subscribes to whichever ones it cares about; absent subscriptions are no-ops.

| Hook | Signature | Semantics |
|---|---|---|
| `OnInbound` | `func(*Inbound) (*Inbound, drop bool)` | Called for every channel-emitted inbound message after debounce/dedup but before routing. Plugins can mutate (e.g. add a transcript), replace, or drop. Plugins are chained by `priority`; the first plugin to set `drop=true` short-circuits. |
| `OnVoiceReceived` | `func(channel string, voice *VoicePayload) (string, error)` | Called only for channel events flagged as voice. First plugin to return a non-empty string wins; that string becomes the inbound's text. STT lives here. |
| `OnOutbound` | `func(*Outbound) (*Outbound, drop bool)` | Called before broker hands a reply to its channel. Same chaining semantics as `OnInbound`. |
| `OnAttach` | `func(*Session, *Mapping)` | Fired after a successful attach claim. Pure observer — return value is ignored. Use for logging, audit trails, custom welcomes. |
| `RegisterTools` | `func(*ToolRegistry)` | Called once at plugin load. Plugins can register MCP tools that adapters then expose to their CLIs. |

The broker calls hooks synchronously and serially within a single inbound/outbound flow — your plugin code blocks the message until it returns. Keep hot-path work fast (target sub-100ms); offload long work to a goroutine and return the message unchanged or with a placeholder marker the plugin will fill in later.

## Skeleton of a compiled-in plugin

A plugin is a Go package whose root file exports a `Register(host *plugin.Host) error` function:

```go
// internal/plugin/builtins/example/example.go
package example

import (
	"context"
	"fmt"

	"github.com/karthikeyan5/c3/internal/plugin"
)

const Name = "example"

type config struct {
	Greeting string `json:"greeting"`
}

func Register(host *plugin.Host) error {
	cfg, err := loadConfig(host)
	if err != nil {
		return fmt.Errorf("%s: load config: %w", Name, err)
	}
	host.OnAttach(func(sess *plugin.Session, mapping *plugin.Mapping) {
		host.Logf("%s: %s attached to %s", Name, sess.CLI, mapping.Name)
	})
	host.RegisterTools(func(reg *plugin.ToolRegistry) {
		reg.Add(plugin.Tool{
			Name:        "example_greet",
			Description: "Send a greeting reply through the attached topic.",
			InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"name": map[string]any{"type": "string"},
				},
			},
			Handler: func(ctx context.Context, args map[string]any) (any, error) {
				name, _ := args["name"].(string)
				return fmt.Sprintf("%s, %s!", cfg.Greeting, name), nil
			},
		})
	})
	return nil
}

func loadConfig(host *plugin.Host) (*config, error) {
	cfg := &config{Greeting: "Hello"}
	if err := host.Config(Name, cfg); err != nil {
		return nil, err
	}
	return cfg, nil
}
```

Then register it in the broker's plugin manifest list (`internal/plugin/registry.go` or equivalent — exact file determined during implementation):

```go
import "github.com/karthikeyan5/c3/internal/plugin/builtins/example"

var Builtins = []plugin.Registrar{
	{Name: stt.Name, Register: stt.Register},
	{Name: example.Name, Register: example.Register},
}
```

Rebuild via `/c3-build` (or `make build`) and the new plugin loads on next broker start.

## Configuration

Plugin config lives at `~/.config/c3/mappings.json` under `plugins.<name>`. The host gives you a typed read via `host.Config(name, &target)`:

```json
{
  "plugins": {
    "example": {
      "enabled": true,
      "priority": 50,
      "greeting": "Hi"
    }
  }
}
```

Reserved fields:

- `enabled` (bool, default `true`) — when `false`, the host skips the plugin's hook subscriptions entirely. The plugin's `Register` is still called once at boot, so it should not assume `enabled=true` to do work outside of subscriptions.
- `priority` (int, default `100`) — chained-hook ordering for `OnInbound` / `OnOutbound`. Lower runs first. STT defaults to `10` because transcription should land before any other plugin sees the inbound.

Anything else under `plugins.<name>` is yours.

## Persistent state

Beyond config, plugins occasionally need runtime state (caches, dedup tables, last-run timestamps). Use `host.State(name)` which returns a JSON-backed `*StateDir` rooted at `~/.config/c3/state/<plugin-name>/`. Treat this as small-data only — sub-megabyte, easily regenerable. For real datasets (model weights, indices), use `host.CacheDir(name)` rooted at `$XDG_CACHE_HOME/c3/<plugin-name>/`.

Two reasons for the split: state is sometimes worth backing up (XDG_CONFIG), cache never is (XDG_CACHE).

## Tools you can register

Tools registered via `RegisterTools` show up in **every adapter's** tool list automatically — Claude Code sees them, Codex sees them. The broker takes care of routing tool calls back to your plugin handler.

Tool name conventions: prefix with the plugin name (`example_greet`, `stt_retranscribe`) so users and the LLM can tell which plugin a tool came from.

The `Handler` function is called with the JSON arguments parsed against your schema. Return value is rendered back to the CLI in the standard MCP `content[].text` shape — strings work; structured returns are JSON-encoded.

For tools that need to send Telegram replies (or interact with another channel), get a handle via `host.Channel(name)`:

```go
ch, err := host.Channel("telegram")
if err == nil {
    ch.SendReply(plugin.ReplyArgs{ChatID: chatID, Text: "..."})
}
```

The plugin should not assume any specific channel exists — degrade gracefully if it doesn't.

## Logging and observability

`host.Logf(format, args...)` writes to the broker's structured log. Don't `fmt.Println` — broker uses stdout for IPC.

Metrics: `host.Metric("plugin.example.invocations", 1, tags...)` if you need observability. Backend is no-op in v1; spec hook is in place for later.

## STT as a worked example

The STT plugin (`internal/plugin/builtins/stt/`) is the v1 shipped example. Skim it for a real implementation:

- Subscribes to `OnVoiceReceived` only.
- Reads `plugins.stt.{language, vocabulary_file, model_path}` config.
- On invocation, downloads the voice attachment via `host.Channel("telegram").DownloadAttachment(file_id)`, then invokes the existing Python whisper pipeline as a subprocess (`stt/transcribe.py`) with the file path and config.
- Returns the transcript string. The broker substitutes it for the voice payload's text on the inbound that gets routed to the CLI.
- Errors degrade to empty string — the channel falls back to its caption-or-`(voice message)` default.

The Python pipeline is a deliberate scope choice; STT is the only first-party plugin that uses a non-Go runtime. New plugins should default to pure Go.

## External subprocess plugins (v1.x — not yet implemented)

Documented for forward-compatibility. A future external plugin lives at `~/.config/c3/plugins/<name>/manifest.json`:

```json
{
  "name": "ocr",
  "executable": "./ocr-plugin",
  "subscribes": ["OnInbound"],
  "config_schema": "./schema.json"
}
```

The broker spawns the executable on startup, sends newline-delimited JSON-RPC over stdio with one method per hook. Same hook signatures as the compiled-in interface, JSON-encoded.

This is not in v1. If you need an externally-distributed plugin before the protocol lands, fork the broker and add your plugin under `internal/plugin/builtins/`.

## Testing

A plugin should ship with Go tests under the same package. The host exposes a `plugin.MockHost()` you can pass to `Register` to drive hooks without spinning up the real broker:

```go
func TestExample_GreetTool(t *testing.T) {
	host := plugin.MockHost(t)
	if err := example.Register(host); err != nil {
		t.Fatalf("Register: %v", err)
	}
	tool, ok := host.Tools()["example_greet"]
	if !ok {
		t.Fatal("example_greet not registered")
	}
	got, err := tool.Handler(t.Context(), map[string]any{"name": "Karthi"})
	if err != nil {
		t.Fatal(err)
	}
	if got != "Hello, Karthi!" {
		t.Errorf("got %q", got)
	}
}
```

The mock host gives you in-memory channels, a captured log, and helpers to fire hook events synthetically.

## Checklist for new plugins

- [ ] Package under `internal/plugin/builtins/<name>/` (or external manifest + binary)
- [ ] `Register(host *plugin.Host) error` exported
- [ ] Hook subscriptions deterministic (no goroutines spawned in `Register` that subscribe later)
- [ ] Config types defined and read via `host.Config`
- [ ] `enabled`/`priority` honored
- [ ] Tools (if any) prefix-namespaced with the plugin name
- [ ] No `fmt.Println` / `os.Stdout.Write` — use `host.Logf`
- [ ] Tests using `plugin.MockHost`
- [ ] Registered in `internal/plugin/registry.go` `Builtins` list
