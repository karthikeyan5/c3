package broker

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/karthikeyan5/c3/internal/c3types"
)

func TestWriteHealthFile_EmptySnapshot(t *testing.T) {
	hf := filepath.Join(t.TempDir(), "health.json")
	t.Setenv("C3_HEALTH_FILE", hf)
	b := newTestBroker()
	b.WriteHealthFile()
	data, err := os.ReadFile(hf)
	if err != nil {
		t.Fatalf("read health file: %v", err)
	}
	if strings.TrimSpace(string(data)) != "{}" {
		t.Errorf("empty snapshot health file = %q, want {}", string(data))
	}
}

func TestWriteHealthFile_DownEntry(t *testing.T) {
	hf := filepath.Join(t.TempDir(), "health.json")
	t.Setenv("C3_HEALTH_FILE", hf)
	b := newTestBroker()
	b.setLastHealth(c3types.HealthEvent{
		Channel: "telegram", State: c3types.HealthStateDown,
		Since: time.Unix(1718722680, 0), Consec: 3, Reason: "dial failures",
	})
	b.WriteHealthFile()
	data, err := os.ReadFile(hf)
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]healthFileEntry
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("health file not valid JSON: %v (%s)", err, data)
	}
	tg, ok := got["telegram"]
	if !ok || tg.State != "down" || tg.SinceUnix != 1718722680 || tg.Consec != 3 {
		t.Errorf("telegram entry = %+v, want down/1718722680/3", tg)
	}
	if tg.SinceHHMM == "" {
		t.Error("since_hhmm should be populated")
	}
}

func TestWriteHealthFile_ConcurrentEdgesProduceValidJSON(t *testing.T) {
	hf := filepath.Join(t.TempDir(), "health.json")
	t.Setenv("C3_HEALTH_FILE", hf)
	b := newTestBroker()
	b.setLastHealth(c3types.HealthEvent{Channel: "telegram", State: c3types.HealthStateDown, Since: time.Now(), Consec: 3})
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); b.WriteHealthFile() }()
	}
	wg.Wait()
	data, err := os.ReadFile(hf)
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]healthFileEntry
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("after concurrent writes, health file not valid JSON: %v (%s)", err, data)
	}
}
