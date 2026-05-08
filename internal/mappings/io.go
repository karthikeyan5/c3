package mappings

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
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

// Write atomically rewrites the mappings file at path. The file is created
// (or replaced) at mode 0600 because it contains the bot token. Atomicity is
// achieved by writing to a sibling tempfile and then renaming.
//
// Backup-on-rewrite: if path already exists, its current contents are copied
// to path+".bak" (mode 0600) before the rename. Spec §4.3 retention rule:
// one generation only — successive rewrites overwrite the same .bak.
func Write(path string, mf *MappingsFile) error {
	data, err := json.MarshalIndent(mf, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal mappings: %w", err)
	}

	// Backup current file if present, BEFORE we touch the destination.
	if _, err := os.Stat(path); err == nil {
		if err := backupFile(path, path+".bak"); err != nil {
			return fmt.Errorf("backup %s -> %s.bak: %w", path, path, err)
		}
	}

	tmp, err := os.CreateTemp(filepath.Dir(path), ".mappings.*.tmp")
	if err != nil {
		return fmt.Errorf("create tempfile: %w", err)
	}
	tmpPath := tmp.Name()
	cleanup := func() { _ = os.Remove(tmpPath) }
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		cleanup()
		return fmt.Errorf("write tempfile: %w", err)
	}
	if err := tmp.Chmod(0600); err != nil {
		_ = tmp.Close()
		cleanup()
		return fmt.Errorf("chmod tempfile: %w", err)
	}
	if err := tmp.Close(); err != nil {
		cleanup()
		return fmt.Errorf("close tempfile: %w", err)
	}
	if err := os.Rename(tmpPath, path); err != nil {
		cleanup()
		return fmt.Errorf("rename %s -> %s: %w", tmpPath, path, err)
	}
	return nil
}

// backupFile copies src to dst, mode 0600, overwriting dst if it exists. Uses
// io.Copy to avoid loading the whole file into memory unnecessarily, though
// mappings.json is tiny in practice.
func backupFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0600)
	if err != nil {
		return err
	}
	defer out.Close()
	if _, err := io.Copy(out, in); err != nil {
		return err
	}
	return nil
}
