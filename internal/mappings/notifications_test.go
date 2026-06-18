package mappings

import (
	"encoding/json"
	"testing"
)

func TestInvasiveNotifications_Default(t *testing.T) {
	// absent block ⇒ true
	if got := (&MappingsFile{SchemaVersion: 1}).InvasiveNotifications(); !got {
		t.Errorf("absent notifications: got %v, want true", got)
	}
	// present but empty ⇒ true
	if got := (&MappingsFile{Notifications: &NotificationsConfig{}}).InvasiveNotifications(); !got {
		t.Errorf("empty notifications: got %v, want true", got)
	}
	// explicit false ⇒ false
	no := false
	if got := (&MappingsFile{Notifications: &NotificationsConfig{Invasive: &no}}).InvasiveNotifications(); got {
		t.Errorf("explicit false: got %v, want false", got)
	}
	// explicit true ⇒ true
	yes := true
	if got := (&MappingsFile{Notifications: &NotificationsConfig{Invasive: &yes}}).InvasiveNotifications(); !got {
		t.Errorf("explicit true: got %v, want true", got)
	}
	// nil receiver ⇒ true (defensive)
	var nilmf *MappingsFile
	if got := nilmf.InvasiveNotifications(); !got {
		t.Errorf("nil receiver: got %v, want true", got)
	}
}

func TestInvasiveNotifications_JSONParse(t *testing.T) {
	var absent MappingsFile
	if err := json.Unmarshal([]byte(`{"schema_version":1,"channels":{},"mappings":{}}`), &absent); err != nil {
		t.Fatal(err)
	}
	if !absent.InvasiveNotifications() {
		t.Error("absent notifications block parsed: want invasive=true")
	}
	var off MappingsFile
	if err := json.Unmarshal([]byte(`{"schema_version":1,"notifications":{"invasive":false}}`), &off); err != nil {
		t.Fatal(err)
	}
	if off.InvasiveNotifications() {
		t.Error("invasive:false parsed: want false")
	}
}
