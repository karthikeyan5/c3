# systemd supervision (optional)

By default the C3 broker is spawned on demand by the first adapter (when you
launch `claude`/`codex`) and stays up as a singleton. That covers the common
case, but a broker that **crashes while no CLI session is open** stays down
until the next launch — inbound Telegram is silently dead in the meantime.

`c3-broker.service` is an **opt-in** `systemd --user` unit that supervises the
broker so it auto-restarts even with no session open (closes recovery-audit
finding `broker-lifecycle-3`). It complements the in-process panic supervision
(the poll goroutines recover + restart themselves) and the health.json
broker-liveness the status line reads.

## Install

```bash
mkdir -p ~/.config/systemd/user
cp docs/systemd/c3-broker.service ~/.config/systemd/user/
# If your GOBIN isn't ~/go/bin, edit ExecStart= first (go env GOBIN GOPATH).
systemctl --user daemon-reload
systemctl --user enable --now c3-broker.service
# Survive logout (so the broker runs even when you're not logged in):
loginctl enable-linger "$USER"
```

Verify: `systemctl --user status c3-broker` and `c3-broker status`.

## How it coexists with adapter auto-spawn

The broker is a singleton (flock + listen-socket). With this unit enabled,
systemd starts the broker at login (winning the lock); adapter `spawnBroker`
calls then find it already running and their spawn exits 0 harmlessly. If the
supervised broker dies, `Restart=always` brings it back. A deliberate
`systemctl --user stop c3-broker` is a commanded stop and is NOT auto-restarted.

## Uninstall

```bash
systemctl --user disable --now c3-broker.service
rm ~/.config/systemd/user/c3-broker.service
systemctl --user daemon-reload
```
