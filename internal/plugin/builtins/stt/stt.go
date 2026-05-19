// Package stt is the v1 STT plugin for C3. It's a thin Go shim that
// subprocesses an external Python handler when a voice attachment arrives.
//
// Spec §6.2 (revised): the existing Python POC's stt-handler.py is mature
// (multi-provider chain with vocabulary support) and lives at the path the
// Python broker installs symlink to. The Go shim defaults to that same path
// and lets the user override via mappings.json:plugins.stt.handler_path.
//
// The handler's contract:
//
//	stdin (line 1):  <bot_token>\n
//	argv:            python3 stt-handler.py <chat_id> <reply_msg_id> <file_id> [<message_thread_id>]
//
// The bot token is supplied on stdin (not argv) so it doesn't appear in
// `ps`/`/proc/<pid>/cmdline`/audit logs. Addresses code-review-2026-05-15
// MAJOR #1 (cli.md §1.10).
//
// On success: prints transcript to stdout (and may also echo back to Telegram
// itself — that's POC-side behavior we don't override). On failure: empty
// stdout. The Go shim returns the trimmed stdout as the transcript.
package stt

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/karthikeyan5/c3/internal/c3types"
	"github.com/karthikeyan5/c3/internal/plugin"
)

// Name is the plugin identifier and the mappings.json:plugins key.
const Name = "stt"

// Config is the plugin's slice of mappings.json:plugins.stt.
type Config struct {
	Enabled     bool   `json:"enabled"`
	Priority    int    `json:"priority"`
	HandlerPath string `json:"handler_path"`    // path to Python script
	Timeout     int    `json:"timeout_seconds"` // subprocess budget (default 300s — long voice notes
	//                                              need download + Gemini + Sarvam fallback time)
}

// defaultTimeoutSeconds is the broker's hard deadline for the STT subprocess.
// 60s was too short — 2026-05-09: a 6m25s voice note (7.9 MB) ate
// 45s on download alone, leaving only 15s for the gemini/sarvam chain →
// SIGKILL'd before either provider returned. 300s gives room for slow
// downloads + a long-audio transcription cycle without surprising the user.
const defaultTimeoutSeconds = 300

// Register subscribes the plugin's OnVoiceReceived callback. Called once at
// broker startup if mappings.json:plugins.stt.enabled (default true).
func Register(host plugin.Host) error {
	cfg := Config{Enabled: true, Timeout: defaultTimeoutSeconds}
	if err := host.Config(Name, &cfg); err != nil {
		return fmt.Errorf("stt: read config: %w", err)
	}
	if !cfg.Enabled {
		host.Logf("stt: plugin disabled via mappings.json:plugins.stt.enabled=false")
		return nil
	}
	if cfg.HandlerPath == "" {
		cfg.HandlerPath = defaultHandlerPath()
	}
	if cfg.Timeout <= 0 {
		cfg.Timeout = defaultTimeoutSeconds
	}
	// Handler existence is checked PER-CALL (inside the callback) rather than
	// once at startup. Two reasons:
	//   1. A missing handler at startup used to silently disable transcription,
	//      so voice messages reached the agent as a bare "(voice message)"
	//      placeholder with no indication anything had gone wrong.
	//   2. With the per-call check, if the user restores the script, the very
	//      next voice message transcribes — no broker restart required.
	// We still log once at startup so the operator knows the current state.
	if _, err := os.Stat(cfg.HandlerPath); err != nil {
		host.Logf("stt: handler %s missing at startup (%v); voice messages will surface [STT FAILED: handler_missing] until the handler is restored",
			cfg.HandlerPath, err)
	} else {
		host.Logf("stt: registered with handler=%s timeout=%ds", cfg.HandlerPath, cfg.Timeout)
	}

	// TODO #12 (2026-05-16): belt-and-suspenders for the fresh-install STT
	// crash. The Python handler also mkdir's these on its own at import
	// time (see plugins/c3/stt/stt-handler.py), but doing it here too means
	// even if the user is running an older bundled handler, the broker
	// has already created the default dirs before the first voice arrives.
	// We only know the *default* paths; if the operator overrides via
	// STT_INBOX_DIR / STT_LOG_FILE in the handler's env, only the Python
	// side's mkdir covers those — that's fine, the Python side is
	// authoritative for handler-side paths.
	ensureSTTDefaultDirs(host)

	// Resolve telegram channel for the bot token. We need the token because
	// the POC handler shells out to Telegram's getFile API itself; the channel
	// owns the only authoritative copy of the token.
	host.OnVoiceReceived(func(ctx context.Context, p c3types.VoicePayload) (string, error) {
		if _, err := os.Stat(cfg.HandlerPath); err != nil {
			host.Logf("stt: msg=%d handler missing at %s (%v)", p.MessageID, cfg.HandlerPath, err)
			return sttFailureMarker("handler_missing"), nil
		}
		token, err := readTelegramToken(host)
		if err != nil {
			host.Logf("stt: token read failed for msg=%d: %v", p.MessageID, err)
			return sttFailureMarker("token_unavailable"), nil
		}
		return runHandler(ctx, host, cfg, token, p)
	})
	return nil
}

// sttFailureMarker is the stand-in transcript text the broker forwards when
// the STT chain fails. It replaces the previous silent (voice message)
// fallback so the receiver knows transcription didn't run and can resend.
// 2026-05-09: "if it's not delivered, you can log it"; equivalent
// principle for STT failure — surface, don't swallow.
//
// 2026-05-18 (#13): include a pointer to the broker log so a fresh-install
// user knows where the actual traceback lives. The marker keeps the
// machine-parseable "[STT FAILED: <reason>]" shape and appends a
// "(see <path>)" hint in human-readable form.
func sttFailureMarker(reason string) string {
	return "[STT FAILED: " + reason + " — see " + sttLogHintPath() + "]"
}

// sttLogHintPath returns the broker log path to surface in the failure
// marker. Mirrors broker.LogPath() but lives here to avoid the
// plugin->broker import cycle. Falls back to the documented default if
// $HOME isn't readable (unlikely; if it happens we'd rather show *a*
// path than an empty string).
func sttLogHintPath() string {
	if env := os.Getenv("C3_LOG_FILE"); env != "" {
		return env
	}
	state := os.Getenv("XDG_STATE_HOME")
	if state == "" {
		home, err := os.UserHomeDir()
		if err != nil || home == "" {
			return "~/.local/state/c3/broker.log"
		}
		state = filepath.Join(home, ".local", "state")
	}
	return filepath.Join(state, "c3", "broker.log")
}

func runHandler(ctx context.Context, host plugin.Host, cfg Config, token string, p c3types.VoicePayload) (string, error) {
	// argv: <chat_id> <msg_id> <file_id> [<thread_id>]
	// token is fed via stdin (see package doc).
	args := []string{
		cfg.HandlerPath,
		strconv.FormatInt(p.ChatID, 10),
		strconv.FormatInt(p.MessageID, 10),
		p.FileID,
	}
	if p.TopicID != nil {
		args = append(args, strconv.FormatInt(*p.TopicID, 10))
	} else {
		args = append(args, "")
	}

	timeout := time.Duration(cfg.Timeout) * time.Second
	tctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	start := time.Now()
	cmd := exec.CommandContext(tctx, "python3", args...)
	cmd.Stdin = strings.NewReader(token + "\n")
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	elapsed := time.Since(start)

	if err != nil {
		// Distinguish timeout from other errors — the caller (and the user)
		// benefit from knowing which.
		reason := "error"
		if tctx.Err() == context.DeadlineExceeded {
			reason = "timeout"
		} else if strings.Contains(err.Error(), "signal: killed") {
			reason = "killed"
		}
		// stderr tail (last 240 chars) helps diagnose without dumping the
		// full provider output. Audio bytes etc. never appear here.
		serr := bytes.TrimSpace(stderr.Bytes())
		if len(serr) > 240 {
			serr = serr[len(serr)-240:]
		}
		host.Logf("stt: msg=%d %s after %v (timeout=%v, file_size=%d): %v | stderr-tail=%q",
			p.MessageID, reason, elapsed.Round(time.Millisecond), timeout,
			p.Size, err, string(serr))
		return sttFailureMarker(reason), nil
	}

	transcript := string(bytes.TrimSpace(stdout.Bytes()))
	if transcript == "" {
		host.Logf("stt: msg=%d empty transcript after %v (no provider returned text)",
			p.MessageID, elapsed.Round(time.Millisecond))
		return sttFailureMarker("empty"), nil
	}
	host.Logf("stt: msg=%d transcribed in %v (chars=%d)",
		p.MessageID, elapsed.Round(time.Millisecond), len(transcript))
	return transcript, nil
}

// readTelegramToken pulls the bot token from mappings.json via the host's
// ChannelConfig helper. The plugin doesn't store its own copy.
func readTelegramToken(host plugin.Host) (string, error) {
	var cc struct {
		BotToken string `json:"bot_token"`
	}
	if err := host.ChannelConfig("telegram", &cc); err != nil {
		return "", fmt.Errorf("stt: read telegram channel config: %w", err)
	}
	if cc.BotToken == "" {
		return "", fmt.Errorf("stt: bot_token is empty in mappings.json:channels.telegram")
	}
	return cc.BotToken, nil
}

// ensureSTTDefaultDirs creates the default handler-side log and inbox
// directories at broker startup. The Python handler also mkdir's these
// at import time; this is belt-and-suspenders so a fresh install can't
// surface the cryptic FileNotFoundError->[STT FAILED: error] failure
// mode if the user is somehow running an older handler bundle.
//
// Failures are logged and swallowed — broker startup must never be
// blocked by an inability to pre-create a plugin scratch dir.
func ensureSTTDefaultDirs(host plugin.Host) {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		host.Logf("stt: skipping default-dir precreate (no home dir): %v", err)
		return
	}
	for _, d := range []string{
		filepath.Join(home, ".claude", "channels", "telegram", "inbox"),
		filepath.Join(home, ".claude", "channels", "telegram"), // for stt-handler.log
	} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			host.Logf("stt: failed to pre-create %s: %v (handler will retry at import)", d, err)
		}
	}
}

// defaultHandlerPath returns the path to the bundled handler shipped under
// `plugins/c3/stt/stt-handler.py` inside the plugin install directory. The
// plugin install root is conveyed via `$CLAUDE_PLUGIN_ROOT`, which Claude
// Code sets when launching the c3 adapter; the adapter inherits the env
// when it spawns the broker, so the broker sees the same root.
//
// Returns "" if `$CLAUDE_PLUGIN_ROOT` isn't set. Operators who run
// `c3-broker` outside Claude Code (manual daemon, systemd unit, etc.)
// must set `plugins.stt.handler_path` in `~/.config/c3/mappings.json`
// explicitly — that's the only resolution rule, and the user-override
// path always wins when set. There is intentionally no fallback to
// pre-c3 legacy paths; behavior must be predictable from config + env.
func defaultHandlerPath() string {
	root := os.Getenv("CLAUDE_PLUGIN_ROOT")
	if root == "" {
		return ""
	}
	return filepath.Join(root, "stt", "stt-handler.py")
}
