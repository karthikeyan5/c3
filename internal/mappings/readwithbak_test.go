package mappings

import (
	"os"
	"path/filepath"
	"testing"
)

const validMappings = `{"schema_version":1}`
const invalidSchema = `{"schema_version":2}`
const malformedJSON = `{"schema_version":1`

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0600); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func TestReadWithBak_PrimaryGood(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "mappings.json")
	writeFile(t, p, validMappings)
	mf, usedBak, err := ReadWithBak(p)
	if err != nil || usedBak || mf == nil {
		t.Fatalf("good primary: got mf=%v usedBak=%v err=%v; want valid mf, usedBak=false, nil", mf, usedBak, err)
	}
}

func TestReadWithBak_MissingPrimaryReturnsIsNotExist(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "mappings.json")
	_, usedBak, err := ReadWithBak(p)
	if !os.IsNotExist(err) {
		t.Fatalf("missing primary: err=%v; want an os.IsNotExist error (caller seeds skeleton)", err)
	}
	if usedBak {
		t.Fatal("missing primary must not report usedBak")
	}
}

func TestReadWithBak_MalformedPrimaryFallsBackToBak(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "mappings.json")
	writeFile(t, p, malformedJSON)
	writeFile(t, p+".bak", validMappings)
	mf, usedBak, err := ReadWithBak(p)
	if err != nil || !usedBak || mf == nil {
		t.Fatalf("malformed primary + good .bak: got mf=%v usedBak=%v err=%v; want .bak used", mf, usedBak, err)
	}
}

func TestReadWithBak_InvalidPrimaryFallsBackToBak(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "mappings.json")
	writeFile(t, p, invalidSchema) // parses, fails Validate
	writeFile(t, p+".bak", validMappings)
	mf, usedBak, err := ReadWithBak(p)
	if err != nil || !usedBak || mf == nil {
		t.Fatalf("invalid primary + good .bak: got mf=%v usedBak=%v err=%v; want .bak used", mf, usedBak, err)
	}
}

func TestReadWithBak_MalformedPrimaryNoBakSurfacesError(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "mappings.json")
	writeFile(t, p, malformedJSON)
	mf, usedBak, err := ReadWithBak(p)
	if err == nil {
		t.Fatal("malformed primary, no .bak: expected an error")
	}
	if os.IsNotExist(err) {
		t.Fatal("a malformed (present) primary must NOT report IsNotExist (would seed a skeleton over real-but-broken config)")
	}
	if usedBak || mf != nil {
		t.Fatalf("got mf=%v usedBak=%v; want nil/false", mf, usedBak)
	}
}

func TestReadWithBak_BothBadSurfacesPrimaryError(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "mappings.json")
	writeFile(t, p, malformedJSON)
	writeFile(t, p+".bak", invalidSchema)
	_, usedBak, err := ReadWithBak(p)
	if err == nil || usedBak {
		t.Fatalf("both bad: got usedBak=%v err=%v; want an error and usedBak=false", usedBak, err)
	}
}
