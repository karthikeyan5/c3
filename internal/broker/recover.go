package broker

import (
	"log"
	"runtime"
)

// recoverGoroutine recovers a panic in a long-lived/background broker code path,
// logging the panic + stack to broker.log instead of letting it crash the whole
// process. An unrecovered panic in ANY goroutine takes down the ENTIRE broker —
// and silently: a goroutine panic is not the runDaemon return-err path (so it
// never reaches broker.log via the FATAL logger), and an adapter-spawned
// broker's stderr is /dev/null. That is the exact silent-death class the
// recovery work targets, so every panic-prone broker goroutine/worker unit must
// guard itself. Telegram's own goroutines are guarded separately in the channel
// layer (superviseLoop/runGuarded); this is the broker-side equivalent.
//
// Use as the FIRST deferred call in a panic-prone unit:
//
//	defer recoverGoroutine("worker.flushInbounds")
//
// Recovering returns the unit to its caller (e.g. the worker run loop), which
// continues — the one poisoned job is skipped and the worker + broker survive.
// recover() must be called DIRECTLY by the deferred function, so this cannot
// delegate the recover() to a shared helper (only the formatting is shared).
func recoverGoroutine(what string) {
	if r := recover(); r != nil {
		logPanic(what, r)
	}
}

// recoverGoroutineThen is recoverGoroutine plus an onPanic cleanup that runs
// ONLY when a panic was recovered. Used where a bare recover would leave a
// caller blocked — e.g. dispatchOutbound must still send a failure result on its
// ResultCh so the waiting tool-call doesn't hang.
func recoverGoroutineThen(what string, onPanic func()) {
	if r := recover(); r != nil {
		logPanic(what, r)
		if onPanic != nil {
			onPanic()
		}
	}
}

// logPanic writes the panic + a single-goroutine stack to broker.log. Not a
// deferred function itself — called by the recover helpers above.
func logPanic(what string, r any) {
	buf := make([]byte, 8192)
	n := runtime.Stack(buf, false)
	log.Printf("PANIC recovered in %s: %v\n%s", what, r, buf[:n])
}
