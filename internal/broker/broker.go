package broker

import (
	"github.com/karthikeyan5/c3/internal/mappings"
)

// Broker holds the in-memory state shared by all connections: stubs registry,
// routes table, and a snapshot of the mappings.json config.
//
// Phase 3 scope: read-only mappings. Write-back lands when channels create
// topics. See spec §4.2.
type Broker struct {
	Mappings *mappings.MappingsFile
	Stubs    *StubRegistry
	Routes   *Routes
}

// New returns a Broker with empty registries and the given mappings config.
func New(mf *mappings.MappingsFile) *Broker {
	return &Broker{
		Mappings: mf,
		Stubs:    NewStubRegistry(),
		Routes:   NewRoutes(),
	}
}
