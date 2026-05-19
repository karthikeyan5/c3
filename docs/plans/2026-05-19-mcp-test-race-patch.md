# Plan — patch flaky data race in `internal/mcp/server_test.go`

**Date:** 2026-05-19
**Scope:** Tiny test-only patch. Five-line change. Holding action only — the deletion-of-package decision (see MORNING-REVIEW DECIDE entry) remains Karthi's call.

## Problem

`TestServer_NotifyConcurrentWithDispatch` (`internal/mcp/server_test.go:89`) shares one `bytes.Buffer` between the server's `Run` goroutine and N concurrent test-side `Notify` goroutines.

Server-side `writeJSON` IS mutex-guarded (`s.wmu`) — but it locks before calling `s.w.Write(...)`, and `s.w` here is `*bytes.Buffer`. `bytes.Buffer` is not goroutine-safe; the race detector flags the buffer's internal slice header even when callers serialize via a mutex.

Reproduction (red step, confirmed 2026-05-19): `go test -count=20 -race ./internal/mcp/` → race detector fires on `TestServer_NotifyConcurrentWithDispatch`.

## Fix

Add a small `syncBuffer` helper to the test file. It wraps a `bytes.Buffer` with its own mutex on `Write` (so the buffer's slice header is touched under a lock) and a `String()` accessor that locks before snapshotting. Pass `&syncBuffer{}` instead of `&bytes.Buffer{}` to `New(...)` in the concurrent test only.

Why only the concurrent test? The other tests (`Roundtrip`, `RoundtripContentLengthFraming`, `NotificationProducesNoResponse`) are single-goroutine — the server's `Run` goroutine is the only writer; no concurrent reader/writer of the buffer exists, so a raw `bytes.Buffer` is fine.

We do NOT touch `server.go`. The server's mutex is correct; the test was careless about the writer it passed in.

## Patch shape

```go
type syncBuffer struct {
    mu  sync.Mutex
    buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
    b.mu.Lock(); defer b.mu.Unlock()
    return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
    b.mu.Lock(); defer b.mu.Unlock()
    return b.buf.String()
}
```

Inside `TestServer_NotifyConcurrentWithDispatch`:
- replace `var out bytes.Buffer` → `out := &syncBuffer{}`
- pass `out` (already a `*syncBuffer`, which is an `io.Writer`) to `New(...)`
- replace `out.String()` → `out.String()` (same call, now mutex-guarded)

## Verification gate

1. `go test -count=20 -race ./internal/mcp/` → 20/20 PASS (green step)
2. `go test -count=1 -race ./...` → ALL PASS (no neighbour regression)
3. `go vet ./...` → clean
4. `go build ./...` → clean

If any OTHER race surfaces during verification: STOP and report to Karthi. Do not fix neighbours.

## Out of scope

- Deleting `internal/mcp/` — Karthi-only decision, will happen separately.
- Changing `server.go`.
- Touching any file other than `internal/mcp/server_test.go`.
- Committing.

## Followup

After patch lands: append a short note under the existing `DECIDE — flaky data race in internal/mcp/server_test.go` section in `MORNING-REVIEW-2026-05-19.md` recording the patch. KEEP the deletion-of-package DECIDE entry intact — Karthi still owes that call.
