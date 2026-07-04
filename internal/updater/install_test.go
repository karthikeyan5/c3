package updater

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"os"
	"path/filepath"
	"testing"
)

// makeTarGz builds a gzip'd tarball at destPath containing entries (name→bytes)
// laid out under a single top-level dir topDir/ — mirroring the release tarball
// layout scripts/package.sh produces. Returns the raw tarball bytes too.
func makeTarGz(t *testing.T, destPath, topDir string, entries map[string][]byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	// Top-level dir entry.
	if err := tw.WriteHeader(&tar.Header{Name: topDir + "/", Typeflag: tar.TypeDir, Mode: 0o755}); err != nil {
		t.Fatal(err)
	}
	for name, data := range entries {
		if err := tw.WriteHeader(&tar.Header{
			Name:     topDir + "/" + name,
			Typeflag: tar.TypeReg,
			Mode:     0o755,
			Size:     int64(len(data)),
		}); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write(data); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	if destPath != "" {
		if err := os.WriteFile(destPath, buf.Bytes(), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return buf.Bytes()
}

func TestInstallBinaries_Success(t *testing.T) {
	src := t.TempDir()
	dest := t.TempDir()

	// Pre-seed dest with OLD binaries.
	for _, name := range BinaryNames {
		writeFile(t, dest, name, []byte("OLD "+name))
	}
	// Stage NEW sources.
	srcPaths := map[string]string{}
	for _, name := range BinaryNames {
		srcPaths[name] = writeFile(t, src, name, []byte("NEW "+name))
	}

	if err := InstallBinaries(dest, srcPaths); err != nil {
		t.Fatalf("install: %v", err)
	}
	for _, name := range BinaryNames {
		got, err := os.ReadFile(filepath.Join(dest, name))
		if err != nil {
			t.Fatalf("read installed %s: %v", name, err)
		}
		if string(got) != "NEW "+name {
			t.Errorf("%s = %q, want NEW", name, got)
		}
		fi, _ := os.Stat(filepath.Join(dest, name))
		if fi.Mode().Perm()&0o100 == 0 {
			t.Errorf("%s is not executable (mode %v)", name, fi.Mode())
		}
	}
	// No leftover temp files in dest.
	ents, _ := os.ReadDir(dest)
	for _, e := range ents {
		if filepath.Ext(e.Name()) == ".tmp" {
			t.Errorf("leftover temp file %s", e.Name())
		}
	}
}

func TestInstallBinaries_FailureMidwayLeavesOriginals(t *testing.T) {
	src := t.TempDir()
	dest := t.TempDir()

	// Pre-seed dest with OLD binaries.
	for _, name := range BinaryNames {
		writeFile(t, dest, name, []byte("OLD "+name))
	}
	// Stage NEW sources — but make ONE of them fail to stage (empty file, which
	// stageBinary rejects). This must abort in Phase 1, before any rename.
	srcPaths := map[string]string{}
	for i, name := range BinaryNames {
		if i == len(BinaryNames)-1 {
			srcPaths[name] = writeFile(t, src, name, []byte{}) // empty → staging error
			continue
		}
		srcPaths[name] = writeFile(t, src, name, []byte("NEW "+name))
	}

	if err := InstallBinaries(dest, srcPaths); err == nil {
		t.Fatal("install with an empty source must fail")
	}
	// Every original must be intact — no partial swap.
	for _, name := range BinaryNames {
		got, err := os.ReadFile(filepath.Join(dest, name))
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		if string(got) != "OLD "+name {
			t.Errorf("%s was modified to %q — originals must be untouched on failure", name, got)
		}
	}
	// And no temp files left behind.
	ents, _ := os.ReadDir(dest)
	for _, e := range ents {
		if filepath.Ext(e.Name()) == ".tmp" {
			t.Errorf("leftover temp file %s after failed install", e.Name())
		}
	}
}

func TestInstallBinaries_MissingSource(t *testing.T) {
	dest := t.TempDir()
	writeFile(t, dest, "c3-broker", []byte("OLD"))
	err := InstallBinaries(dest, map[string]string{"c3-broker": filepath.Join(t.TempDir(), "does-not-exist")})
	if err == nil {
		t.Fatal("missing source must error")
	}
	if got, _ := os.ReadFile(filepath.Join(dest, "c3-broker")); string(got) != "OLD" {
		t.Errorf("original clobbered to %q on missing-source failure", got)
	}
}

func TestExtractTarGz(t *testing.T) {
	work := t.TempDir()
	tarPath := filepath.Join(work, "rel.tar.gz")
	entries := map[string][]byte{}
	for _, name := range BinaryNames {
		entries[name] = []byte("bin-" + name)
	}
	makeTarGz(t, tarPath, "c3_v1.0.0_linux_amd64", entries)

	out := filepath.Join(work, "out")
	if err := os.MkdirAll(out, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := extractTarGz(tarPath, out); err != nil {
		t.Fatalf("extract: %v", err)
	}
	pkg := filepath.Join(out, "c3_v1.0.0_linux_amd64")
	for _, name := range BinaryNames {
		got, err := os.ReadFile(filepath.Join(pkg, name))
		if err != nil {
			t.Fatalf("extracted %s: %v", name, err)
		}
		if string(got) != "bin-"+name {
			t.Errorf("%s = %q", name, got)
		}
	}
}

func TestExtractTarGz_RejectsTraversal(t *testing.T) {
	work := t.TempDir()
	tarPath := filepath.Join(work, "evil.tar.gz")
	// Hand-build a tarball with a traversal entry.
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	payload := []byte("pwned")
	_ = tw.WriteHeader(&tar.Header{Name: "../escape", Typeflag: tar.TypeReg, Mode: 0o755, Size: int64(len(payload))})
	_, _ = tw.Write(payload)
	_ = tw.Close()
	_ = gz.Close()
	if err := os.WriteFile(tarPath, buf.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}

	out := filepath.Join(work, "out")
	_ = os.MkdirAll(out, 0o755)
	if err := extractTarGz(tarPath, out); err == nil {
		t.Fatal("path-traversal entry must be rejected")
	}
	if _, err := os.Stat(filepath.Join(work, "escape")); err == nil {
		t.Fatal("traversal wrote a file outside the destination")
	}
}
