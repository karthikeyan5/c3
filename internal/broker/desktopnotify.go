package broker

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/karthikeyan5/c3/internal/c3types"
)

// healthNotifier raises a local desktop popup for a channel-health edge. An
// interface so tests can substitute a fake without spawning real desktop
// processes (the real impl is *desktopNotifier).
type healthNotifier interface {
	Notify(ev c3types.HealthEvent)
}

// desktopNotifier raises a local desktop popup for a channel-health edge. It is
// ONE of the four out-of-band sinks (the CLI broadcast + the status line are the
// guaranteed backstops if this fails) — a notify failure is logged once and
// NEVER blocks or crashes the broker.
//
// Environment snapshot: the broker is manually launched (not a systemd unit),
// so the launching shell carries DISPLAY / WAYLAND_DISPLAY /
// DBUS_SESSION_BUS_ADDRESS / XDG_RUNTIME_DIR. We snapshot them at broker start
// and reuse them for every exec so a later environment change in the broker
// process can't break the popup. If DBUS_SESSION_BUS_ADDRESS is empty we
// synthesize the well-known per-user bus path (unix:path=$XDG_RUNTIME_DIR/bus).
type desktopNotifier struct {
	bin  string   // resolved binary path; "" disables (no notifier found)
	tool string   // "notify-send" | "zenity"
	env  []string // snapshotted environment for the exec
}

// newDesktopNotifier snapshots the desktop session environment and resolves a
// notification binary once: notify-send (preferred) → zenity → none. Returns a
// notifier whose Notify is a no-op when nothing is available.
func newDesktopNotifier() *desktopNotifier {
	dn := &desktopNotifier{env: snapshotDesktopEnv()}
	if p, err := exec.LookPath("notify-send"); err == nil {
		dn.bin = p
		dn.tool = "notify-send"
	} else if p, err := exec.LookPath("zenity"); err == nil {
		dn.bin = p
		dn.tool = "zenity"
	}
	return dn
}

// snapshotDesktopEnv returns the current process environment with a synthesized
// DBUS_SESSION_BUS_ADDRESS if one isn't already present (and XDG_RUNTIME_DIR is).
// The session vars (DISPLAY/WAYLAND_DISPLAY) ride along inside os.Environ().
func snapshotDesktopEnv() []string {
	env := os.Environ()
	if os.Getenv("DBUS_SESSION_BUS_ADDRESS") == "" {
		if xdg := os.Getenv("XDG_RUNTIME_DIR"); xdg != "" {
			env = append(env, "DBUS_SESSION_BUS_ADDRESS=unix:path="+strings.TrimRight(xdg, "/")+"/bus")
		}
	}
	return env
}

// Notify fires a desktop popup for the given health event. It runs with a 2s
// context timeout + the snapshotted env, and recovers from any panic — a notify
// failure must never propagate. Caller already runs this in its own goroutine
// (see BrokerHost.NotifyHealth), so this is synchronous within that goroutine.
func (dn *desktopNotifier) Notify(ev c3types.HealthEvent) {
	defer func() {
		if r := recover(); r != nil {
			log.Printf("health-notify: desktop notify panic recovered: %v", r)
		}
	}()
	if dn == nil || dn.bin == "" {
		// No notifier available — the CLI broadcast + status line are the
		// backstops. Log once at the per-event tier so its absence is visible.
		log.Printf("health-notify: no desktop notifier (notify-send/zenity) found; relying on CLI broadcast + status")
		return
	}

	title, body, urgency := formatHealthPopup(ev)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	var cmd *exec.Cmd
	switch dn.tool {
	case "notify-send":
		cmd = exec.CommandContext(ctx, dn.bin, "-u", urgency, "-a", "C3", title, body)
	case "zenity":
		// zenity --notification has no urgency/app flags; collapse to one line.
		cmd = exec.CommandContext(ctx, dn.bin, "--notification", "--text", title+" — "+body)
	default:
		return
	}
	cmd.Env = dn.env
	if err := cmd.Run(); err != nil {
		log.Printf("health-notify: desktop notify exec failed (tool=%s): %v", dn.tool, err)
	}
}

// formatHealthPopup renders the title/body/urgency for a health event. DOWN is
// critical urgency; recovery is normal. Body carries the human detail (time +
// fail count / down duration). No bot token or token-bearing URL ever appears.
func formatHealthPopup(ev c3types.HealthEvent) (title, body, urgency string) {
	ch := ev.Channel
	if ch == "" {
		ch = "channel"
	}
	switch ev.State {
	case c3types.HealthStateDown:
		title = fmt.Sprintf("C3: %s fetch DOWN", ch)
		body = fmt.Sprintf("Cannot reach %s since %s (%d %s). Inbound offline — alerts here only.",
			ch, ev.Since.Format("15:04"), ev.Consec, reasonOr(ev.Reason, "failures"))
		urgency = "critical"
	default: // up / recovered
		title = fmt.Sprintf("C3: %s fetch RECOVERED", ch)
		body = fmt.Sprintf("%s reachable again (was down %s).", ch, ev.DownFor.Round(time.Second))
		urgency = "normal"
	}
	return title, body, urgency
}

// reasonOr returns reason when non-empty, else fallback.
func reasonOr(reason, fallback string) string {
	if strings.TrimSpace(reason) == "" {
		return fallback
	}
	return reason
}
