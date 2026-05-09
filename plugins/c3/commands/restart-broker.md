---
description: Restart the C3 broker. Claims survive the bounce as long as adapter PIDs are alive.
---

!pkill -9 -x c3-broker 2>/dev/null; for i in 1 2 3 4 5; do sleep 1; pgrep -x c3-broker >/dev/null || break; done; pgrep -x c3-broker && echo "WARN: broker still alive after kill" || echo "broker stopped"; setsid c3-broker </dev/null >/dev/null 2>>/tmp/c3-broker-stderr.log & disown; for i in 1 2 3 4 5; do sleep 1; test -S /run/user/1000/c3.sock && pgrep -x c3-broker >/dev/null && break; done; pgrep -ax c3-broker

**Important**: do NOT delete the pidfile (`rm -f`). The pidfile holds the singleton flock; deleting it between kill and respawn lets two brokers race past the flock onto separate inodes — both bind the listen socket, adapters scatter, Telegram returns 409. The kill-and-wait loop above gives the kernel time to release the flock on the OLD inode before the new broker tries to acquire on the SAME inode.

Display the output. A healthy result lists exactly ONE broker pid at the end. If more than one shows, run `/c3:status` to diagnose; you may have a parallel adapter that auto-respawned a broker during the kill window — in which case kill the duplicate manually and re-run.
