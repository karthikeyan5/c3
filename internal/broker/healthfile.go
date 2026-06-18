package broker

import (
	"encoding/json"
	"log"
	"os"
	"path/filepath"
)

// healthFileEntry is the per-channel shape written to health.json for the
// Claude Code status line to read. JSON-tagged for the bash reader.
type healthFileEntry struct {
	State     string `json:"state"` // "up" | "down"
	SinceUnix int64  `json:"since_unix,omitempty"`
	SinceHHMM string `json:"since_hhmm,omitempty"` // ev.Since.Format("15:04"), local time
	Reason    string `json:"reason,omitempty"`
	Consec    int    `json:"consec,omitempty"`
}

// WriteHealthFile atomically writes the current per-channel health snapshot to
// HealthFilePath() for the status line to read. Best-effort: any error is
// logged and ignored (the status cache + broker log are the backstops). At
// startup lastHealth is empty, so this writes "{}" — clearing any stale file
// from a prior crash and reading as "no outage" to the status-line script.
// Atomic via CreateTemp-in-same-dir + rename; no fsync (best-effort).
func (b *Broker) WriteHealthFile() {
	snap := b.lastHealthSnapshot()
	out := make(map[string]healthFileEntry, len(snap))
	for ch, ev := range snap {
		out[ch] = healthFileEntry{
			State:     string(ev.State),
			SinceUnix: ev.Since.Unix(),
			SinceHHMM: ev.Since.Format("15:04"),
			Reason:    ev.Reason,
			Consec:    ev.Consec,
		}
	}
	data, err := json.Marshal(out)
	if err != nil {
		log.Printf("health-file: marshal failed: %v", err)
		return
	}
	path := HealthFilePath()
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0700); err != nil {
		log.Printf("health-file: mkdir %s failed: %v", dir, err)
		return
	}
	tmp, err := os.CreateTemp(dir, ".health.*.tmp")
	if err != nil {
		log.Printf("health-file: create temp failed: %v", err)
		return
	}
	tmpPath := tmp.Name()
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpPath)
		log.Printf("health-file: write temp failed: %v", err)
		return
	}
	if err := tmp.Chmod(0600); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpPath)
		log.Printf("health-file: chmod temp failed: %v", err)
		return
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpPath)
		log.Printf("health-file: close temp failed: %v", err)
		return
	}
	if err := os.Rename(tmpPath, path); err != nil {
		_ = os.Remove(tmpPath)
		log.Printf("health-file: rename failed: %v", err)
	}
}
