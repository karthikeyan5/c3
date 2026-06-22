package queue

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/karthikeyan5/c3/internal/c3types"
)

// Status is a per-route snapshot for /status. Pending is lines after the cursor;
// OldestUnix is the timestamp of the oldest pending line (0 when empty).
type Status struct {
	Pending    int
	OldestUnix int64
}

// Store owns the queue directory. It is single-owner per route (the broker
// funnels every route's file ops through that route's RouteWorker goroutine), so
// the file ops hold no per-file locks. Only the cheap cross-route status index
// is mutex-guarded — it touches no files.
type Store struct {
	dir string

	mu  sync.Mutex // guards idx ONLY (the cross-route status counters)
	idx map[RouteKey]Status
}

// NewStore creates the queue dir (0700) and returns a Store. Call
// RecoverOnStartup once after construction to rebuild the index from disk.
func NewStore(dir string) (*Store, error) {
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, fmt.Errorf("queue: mkdir %s: %w", dir, err)
	}
	return &Store{dir: dir, idx: map[RouteKey]Status{}}, nil
}

func (s *Store) jsonlPath(rk RouteKey) string { return filepath.Join(s.dir, rk.File()+".jsonl") }
func (s *Store) curPath(rk RouteKey) string   { return filepath.Join(s.dir, rk.File()+".cur") }

// Append writes one JSON line and fsyncs it (data + parent dir), then refreshes
// the status index. The caller (worker) only treats the source update_id as
// offset-eligible AFTER this returns nil.
func (s *Store) Append(rk RouteKey, in *c3types.Inbound) error {
	data, err := json.Marshal(in)
	if err != nil {
		return fmt.Errorf("queue: marshal: %w", err)
	}
	f, err := os.OpenFile(s.jsonlPath(rk), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0600)
	if err != nil {
		return fmt.Errorf("queue: open append: %w", err)
	}
	if _, err := f.Write(append(data, '\n')); err != nil {
		_ = f.Close()
		return fmt.Errorf("queue: write: %w", err)
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		return fmt.Errorf("queue: fsync: %w", err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("queue: close: %w", err)
	}
	// fsync the parent dir so a freshly O_CREATE'd .jsonl's directory entry is
	// durable across a power-loss crash — otherwise a crash right after Append
	// returns nil could lose the message the broker already treated as persisted
	// (and whose update_id it has since let the Telegram offset advance past).
	if err := s.syncDir(); err != nil {
		return err
	}
	s.refreshIndex(rk)
	return nil
}

// syncDir fsyncs the queue directory so newly-created or renamed entries (new
// .jsonl on first Append, renamed .cur/.jsonl on writeCursor/rewrite) are
// durable across a crash. Mirrors offset_store.go's dir-fsync.
func (s *Store) syncDir() error {
	d, err := os.Open(s.dir)
	if err != nil {
		return fmt.Errorf("queue: open dir for fsync: %w", err)
	}
	if err := d.Sync(); err != nil {
		_ = d.Close()
		return fmt.Errorf("queue: fsync dir: %w", err)
	}
	if err := d.Close(); err != nil {
		return fmt.Errorf("queue: close dir: %w", err)
	}
	return nil
}

// readLines returns the parsed inbound lines and the cursor. Corrupt lines are
// skipped (logged via the returned skipped count is implicit — caller logs).
func (s *Store) readLines(rk RouteKey) (lines []c3types.Inbound, cursor int, err error) {
	f, err := os.Open(s.jsonlPath(rk))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, 0, nil
		}
		return nil, 0, fmt.Errorf("queue: open read: %w", err)
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 8*1024*1024)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			lines = append(lines, c3types.Inbound{}) // keep line-number alignment
			continue
		}
		var in c3types.Inbound
		if jerr := json.Unmarshal([]byte(line), &in); jerr != nil {
			// Corrupt line: keep a placeholder so the cursor's line-number stays
			// aligned with the file, but mark it skippable via a zero MessageID
			// caller filters. We tag it with a sentinel channel so Peek/Consume
			// can drop it.
			lines = append(lines, c3types.Inbound{Channel: corruptSentinel})
			continue
		}
		lines = append(lines, in)
	}
	if serr := sc.Err(); serr != nil {
		return nil, 0, fmt.Errorf("queue: scan: %w", serr)
	}
	cursor = s.readCursor(rk)
	return lines, cursor, nil
}

const corruptSentinel = "\x00corrupt"

// pendingFrom returns the non-corrupt lines after the cursor.
func pendingFrom(lines []c3types.Inbound, cursor int) []c3types.Inbound {
	if cursor < 0 {
		cursor = 0
	}
	if cursor > len(lines) {
		cursor = len(lines)
	}
	out := make([]c3types.Inbound, 0, len(lines)-cursor)
	for _, in := range lines[cursor:] {
		if in.Channel == corruptSentinel {
			continue
		}
		out = append(out, in)
	}
	return out
}

// Peek returns up to n oldest pending messages without advancing the cursor.
func (s *Store) Peek(rk RouteKey, n int) ([]c3types.Inbound, error) {
	lines, cursor, err := s.readLines(rk)
	if err != nil {
		return nil, err
	}
	pending := pendingFrom(lines, cursor)
	if n >= 0 && n < len(pending) {
		pending = pending[:n]
	}
	return pending, nil
}

// Consume returns up to n oldest pending messages AND advances the cursor past
// them (corrupt placeholder lines are stepped over too). Deletes both files when
// the cursor reaches EOF.
func (s *Store) Consume(rk RouteKey, n int) ([]c3types.Inbound, error) {
	lines, cursor, err := s.readLines(rk)
	if err != nil {
		return nil, err
	}
	// n < 0 is the "consume all" sentinel; clamp the capacity hint so a negative
	// n never reaches make() as a cap (which would panic "makeslice: cap out of
	// range"). For "all", size the hint to the worst-case pending count.
	cap0 := n
	if cap0 < 0 {
		cap0 = len(lines) - cursor
	}
	if cap0 < 0 {
		cap0 = 0
	}
	out := make([]c3types.Inbound, 0, cap0)
	pos := cursor
	for pos < len(lines) && (n < 0 || len(out) < n) {
		in := lines[pos]
		pos++
		if in.Channel == corruptSentinel {
			continue // step over corrupt lines, don't return them
		}
		out = append(out, in)
	}
	// Advance the cursor to pos (after the last consumed/skipped line).
	if pos >= len(lines) {
		if err := s.deletePair(rk); err != nil {
			return nil, err
		}
	} else if err := s.writeCursor(rk, pos); err != nil {
		return nil, err
	}
	s.refreshIndex(rk)
	return out, nil
}

// Pending returns the count of pending messages and the oldest pending
// timestamp (zero time when empty).
func (s *Store) Pending(rk RouteKey) (int, time.Time) {
	lines, cursor, err := s.readLines(rk)
	if err != nil {
		return 0, time.Time{}
	}
	pending := pendingFrom(lines, cursor)
	if len(pending) == 0 {
		return 0, time.Time{}
	}
	return len(pending), pending[0].Timestamp
}

// StatusAll returns a snapshot of the in-memory status index (all known routes
// with pending > 0). Cheap; touches no files.
func (s *Store) StatusAll() map[RouteKey]Status {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make(map[RouteKey]Status, len(s.idx))
	for k, v := range s.idx {
		if v.Pending > 0 {
			out[k] = v
		}
	}
	return out
}

// EvictOverCap drops the oldest lines exceeding MaxMessages OR older than MaxAge
// (a cap-only rewrite), adjusting the cursor by the number dropped. Returns the
// count dropped (0 when under cap). Never silent — the broker logs + sends a
// Telegram notice when dropped > 0.
func (s *Store) EvictOverCap(rk RouteKey) (int, error) {
	lines, cursor, err := s.readLines(rk)
	if err != nil || len(lines) == 0 {
		return 0, err
	}
	// rewrite() strips corrupt placeholders from the file, so the cap/age/cursor
	// math must run on the corrupt-free real-line view — otherwise corrupt lines
	// inflate the count (over-evicting) and desync the rewritten file's length
	// from newCursor (risking double-serve or skip). Project lines→real and map
	// the cursor into the same corrupt-free coordinate space.
	real := make([]c3types.Inbound, 0, len(lines))
	cursorReal := 0 // cursor mapped into the corrupt-free index space
	for i, in := range lines {
		if i < cursor && in.Channel != corruptSentinel {
			cursorReal++
		}
		if in.Channel == corruptSentinel {
			continue
		}
		real = append(real, in)
	}
	if len(real) == 0 {
		// Only corrupt lines remained; rewrite drops them all (deletes the pair).
		if err := s.rewrite(rk, real); err != nil {
			return 0, err
		}
		if err := s.deletePair(rk); err != nil {
			return 0, err
		}
		s.refreshIndex(rk)
		return 0, nil
	}
	cutoff := time.Now().Add(-MaxAge)
	// Find how many leading real lines to drop: by age first, then by count.
	drop := 0
	for _, in := range real {
		if !in.Timestamp.IsZero() && in.Timestamp.Before(cutoff) {
			drop++
			continue
		}
		break
	}
	if len(real)-drop > MaxMessages {
		drop += (len(real) - drop) - MaxMessages
	}
	if drop == 0 {
		return 0, nil
	}
	if drop > len(real) {
		drop = len(real)
	}
	kept := real[drop:]
	if err := s.rewrite(rk, kept); err != nil {
		return 0, err
	}
	newCursor := cursorReal - drop
	if newCursor < 0 {
		newCursor = 0
	}
	if newCursor >= len(kept) {
		if err := s.deletePair(rk); err != nil {
			return 0, err
		}
	} else if err := s.writeCursor(rk, newCursor); err != nil {
		return 0, err
	}
	s.refreshIndex(rk)
	return drop, nil
}

// RefreshText rewrites, in place, the Text of the still-pending queued line whose
// Inbound.MessageID == messageID. Used by retranscribe to replace a stored STT-
// failure placeholder with a corrected transcript so a later fetch_queue returns
// the fixed text. Only lines AT/AFTER the cursor (not yet consumed) are eligible —
// an already-consumed or never-queued message_id is a clean no-op. Returns whether
// a line was refreshed.
//
// It reuses the same cap-safe atomic rewrite path as EvictOverCap (temp file →
// fsync → rename → dir-fsync), so the on-disk update is crash-safe. Like that
// path, rewrite() strips corrupt placeholder lines, so the cursor is remapped into
// the corrupt-free coordinate space to stay aligned with the rewritten file.
func (s *Store) RefreshText(rk RouteKey, messageID int64, newText string) (bool, error) {
	if messageID == 0 {
		return false, nil // unidentifiable; never refresh
	}
	lines, cursor, err := s.readLines(rk)
	if err != nil || len(lines) == 0 {
		return false, err
	}
	// Project lines→real (corrupt-free) and map the cursor into that index space,
	// mirroring EvictOverCap so a rewrite that drops corrupt lines doesn't desync
	// the cursor from the rewritten file.
	real := make([]c3types.Inbound, 0, len(lines))
	cursorReal := 0
	for i, in := range lines {
		if in.Channel == corruptSentinel {
			continue
		}
		if i < cursor {
			cursorReal++
		}
		real = append(real, in)
	}
	// Find the matching message among the still-pending (cursor-onward) real lines.
	found := false
	for idx := cursorReal; idx < len(real); idx++ {
		if real[idx].MessageID == messageID {
			real[idx].Text = newText
			found = true
			break
		}
	}
	if !found {
		return false, nil
	}
	if err := s.rewrite(rk, real); err != nil {
		return false, err
	}
	// rewrite() may have dropped corrupt lines that sat before the cursor; persist
	// the remapped cursor so consumed lines stay consumed.
	if cursorReal != cursor {
		if cursorReal >= len(real) {
			if err := s.deletePair(rk); err != nil {
				return false, err
			}
		} else if err := s.writeCursor(rk, cursorReal); err != nil {
			return false, err
		}
	}
	s.refreshIndex(rk)
	return true, nil
}

// RecoverOnStartup scans the queue dir and rebuilds the status index. A route
// whose cursor is at/after its line count has its pair deleted; otherwise the
// index records the derived pending count.
func (s *Store) RecoverOnStartup() error {
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("queue: readdir: %w", err)
	}
	for _, e := range entries {
		name := e.Name()
		if !strings.HasSuffix(name, ".jsonl") {
			continue
		}
		base := strings.TrimSuffix(name, ".jsonl")
		rk, ok := parseRouteFile(base)
		if !ok {
			continue
		}
		lines, cursor, rerr := s.readLines(rk)
		if rerr != nil {
			continue
		}
		if cursor >= len(lines) {
			_ = s.deletePair(rk)
			continue
		}
		s.refreshIndex(rk)
	}
	return nil
}

// --- cursor + file helpers ---

func (s *Store) readCursor(rk RouteKey) int {
	data, err := os.ReadFile(s.curPath(rk))
	if err != nil {
		return 0
	}
	n, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil || n < 0 {
		return 0
	}
	return n
}

func (s *Store) writeCursor(rk RouteKey, n int) error {
	tmp := s.curPath(rk) + ".tmp"
	if err := os.WriteFile(tmp, []byte(strconv.Itoa(n)), 0600); err != nil {
		return fmt.Errorf("queue: write cursor: %w", err)
	}
	if err := os.Rename(tmp, s.curPath(rk)); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("queue: rename cursor: %w", err)
	}
	if err := s.syncDir(); err != nil {
		return err
	}
	return nil
}

func (s *Store) deletePair(rk RouteKey) error {
	_ = os.Remove(s.curPath(rk))
	if err := os.Remove(s.jsonlPath(rk)); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("queue: remove jsonl: %w", err)
	}
	return nil
}

// rewrite atomically replaces the jsonl with the given lines (cap valve only).
func (s *Store) rewrite(rk RouteKey, lines []c3types.Inbound) error {
	tmp := s.jsonlPath(rk) + ".tmp"
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0600)
	if err != nil {
		return fmt.Errorf("queue: open rewrite: %w", err)
	}
	w := bufio.NewWriter(f)
	for _, in := range lines {
		if in.Channel == corruptSentinel {
			continue
		}
		data, merr := json.Marshal(in)
		if merr != nil {
			_ = f.Close()
			_ = os.Remove(tmp)
			return fmt.Errorf("queue: marshal rewrite: %w", merr)
		}
		_, _ = w.Write(append(data, '\n'))
	}
	if err := w.Flush(); err != nil {
		_ = f.Close()
		_ = os.Remove(tmp)
		return fmt.Errorf("queue: flush rewrite: %w", err)
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		_ = os.Remove(tmp)
		return fmt.Errorf("queue: fsync rewrite: %w", err)
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("queue: close rewrite: %w", err)
	}
	if err := os.Rename(tmp, s.jsonlPath(rk)); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("queue: rename rewrite: %w", err)
	}
	if err := s.syncDir(); err != nil {
		return err
	}
	return nil
}

// refreshIndex recomputes the cheap status counters for one route.
func (s *Store) refreshIndex(rk RouteKey) {
	n, oldest := s.Pending(rk)
	s.mu.Lock()
	defer s.mu.Unlock()
	if n == 0 {
		delete(s.idx, rk)
		return
	}
	s.idx[rk] = Status{Pending: n, OldestUnix: oldest.Unix()}
}

// parseRouteFile reverses RouteKey.File(): "<channel>__<chat>__<topic|none>".
func parseRouteFile(base string) (RouteKey, bool) {
	parts := strings.Split(base, "__")
	if len(parts) != 3 {
		return RouteKey{}, false
	}
	chat, err := strconv.ParseInt(parts[1], 10, 64)
	if err != nil {
		return RouteKey{}, false
	}
	rk := RouteKey{Channel: parts[0], ChatID: chat}
	if parts[2] != "none" {
		tid, terr := strconv.ParseInt(parts[2], 10, 64)
		if terr != nil {
			return RouteKey{}, false
		}
		rk.TopicID = &tid
	}
	return rk, true
}
