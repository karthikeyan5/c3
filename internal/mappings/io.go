package mappings

import (
	"encoding/json"
	"fmt"
	"os"
)

// Read parses the mappings.json file at path. Returns os.IsNotExist-friendly
// error if the file is missing.
func Read(path string) (*MappingsFile, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var mf MappingsFile
	if err := json.Unmarshal(data, &mf); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	return &mf, nil
}
