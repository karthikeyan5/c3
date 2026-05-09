---
description: Restart the C3 broker. Claims survive across the bounce as long as adapter PIDs are alive.
---

!pkill -9 c3-broker 2>/dev/null; sleep 1; rm -f /run/user/1000/c3-broker.pid 2>/dev/null; rm -f /tmp/c3-$(id -u)-broker.pid 2>/dev/null; setsid c3-broker </dev/null >/dev/null 2>>/tmp/c3-broker-stderr.log & disown; sleep 2; pgrep -af '\\bc3-broker$' | head -1

Display the output: a healthy result is a single line with the new broker's pid. The new broker re-reads mappings.json, reconnects to Telegram, and the adapter's recoverBroker logic transfers its existing claim to the new connection automatically. If `pgrep` shows nothing, run `/c3:status` to diagnose.
