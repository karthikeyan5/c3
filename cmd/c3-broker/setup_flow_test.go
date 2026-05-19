package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// TestStartBackgroundInstall_DelegatesToRunFn confirms the goroutine
// runs the package-level installRunFn and the result reaches the
// channel. Foundational sanity check for everything else in this file.
func TestStartBackgroundInstall_DelegatesToRunFn(t *testing.T) {
	prev := installRunFn
	t.Cleanup(func() { installRunFn = prev })

	want := installResult{
		err:      errors.New("fake"),
		output:   []byte("fake stderr"),
		duration: 42 * time.Millisecond,
	}
	installRunFn = func(ctx context.Context) installResult { return want }

	ch := startBackgroundInstall(context.Background())
	select {
	case got := <-ch:
		if got.err == nil || got.err.Error() != "fake" {
			t.Errorf("err = %v, want fake", got.err)
		}
		if string(got.output) != "fake stderr" {
			t.Errorf("output = %q, want %q", got.output, "fake stderr")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("startBackgroundInstall channel never produced a result")
	}
}

// TestStartBackgroundInstall_StartsBeforeJoin_ConcurrentExecution
// verifies the install actually runs CONCURRENTLY — i.e. the
// goroutine progresses while the caller is doing other work. This is
// the whole point of items #4+#5: user reads the bot+group walk
// WHILE go install runs.
//
// We use a fake installRunFn that blocks on a channel until released,
// signals progress on another channel. The test starts the install,
// asserts the install has begun (signal received), then releases it.
// The releaser-as-caller mimics the caller doing other work during
// the bot+group walk.
func TestStartBackgroundInstall_StartsBeforeJoin_ConcurrentExecution(t *testing.T) {
	prev := installRunFn
	t.Cleanup(func() { installRunFn = prev })

	started := make(chan struct{})
	release := make(chan struct{})
	installRunFn = func(ctx context.Context) installResult {
		close(started)
		<-release // block until test releases
		return installResult{}
	}

	ch := startBackgroundInstall(context.Background())

	// The install MUST have started by now, even though we haven't
	// joined the channel. This is the concurrency guarantee.
	select {
	case <-started:
		// good — install began without us joining
	case <-time.After(2 * time.Second):
		t.Fatal("install goroutine never started; startBackgroundInstall is not actually concurrent")
	}

	// Now release the fake install and drain.
	close(release)
	select {
	case <-ch:
		// done
	case <-time.After(2 * time.Second):
		t.Fatal("install never produced a result after release")
	}
}

// TestJoinBackgroundInstall_SuccessQuiet — successful build returns
// nil error, no panic. (Side effect: prints "✓ binaries built" to
// stdout; we don't assert on that — Karthi's copy, not load-bearing.)
func TestJoinBackgroundInstall_SuccessQuiet(t *testing.T) {
	ch := make(chan installResult, 1)
	ch <- installResult{duration: 5 * time.Millisecond}
	close(ch)
	if err := joinBackgroundInstall(ch); err != nil {
		t.Errorf("join on success returned err=%v, want nil", err)
	}
}

// TestJoinBackgroundInstall_SkippedNotError — skipped install is NOT
// an error; setup continues. Critical: source-dir-not-found mustn't
// abort setup.
func TestJoinBackgroundInstall_SkippedNotError(t *testing.T) {
	ch := make(chan installResult, 1)
	ch <- installResult{skipped: true}
	close(ch)
	if err := joinBackgroundInstall(ch); err != nil {
		t.Errorf("join on skipped returned err=%v, want nil (skip is not an error)", err)
	}
}

// TestJoinBackgroundInstall_ErrorPropagated — failed install
// surfaces the error so runSetup can warn but continue.
func TestJoinBackgroundInstall_ErrorPropagated(t *testing.T) {
	want := errors.New("simulated: exit status 1")
	ch := make(chan installResult, 1)
	ch <- installResult{err: want, output: []byte("go: undefined: foo")}
	close(ch)
	got := joinBackgroundInstall(ch)
	if !errors.Is(got, want) {
		t.Errorf("join on error returned err=%v, want %v", got, want)
	}
}

// TestDiscoverSourceDir_OverrideHit — $C3_SRC_DIR pointing at a real
// C3 source tree resolves. Uses the test binary's own go.mod walk.
func TestDiscoverSourceDir_OverrideHit(t *testing.T) {
	// Find the C3 root from the test binary's location.
	c3Root := findThisRepoRoot(t)
	t.Setenv("C3_SRC_DIR", c3Root)
	dir, ok := discoverSourceDir()
	if !ok {
		t.Fatalf("discoverSourceDir() = ok=false, want true (C3_SRC_DIR=%s)", c3Root)
	}
	if dir != c3Root {
		t.Errorf("discoverSourceDir() = %q, want %q (override should be authoritative)", dir, c3Root)
	}
}

// TestDiscoverSourceDir_OverrideMissesIsFalse — a bogus
// $C3_SRC_DIR returns (..., false). Setup will then skip the
// background build with a warning rather than abort.
func TestDiscoverSourceDir_OverrideMissesIsFalse(t *testing.T) {
	t.Setenv("C3_SRC_DIR", "/definitely/does/not/exist/path")
	if _, ok := discoverSourceDir(); ok {
		t.Error("discoverSourceDir(bogus override) = ok=true, want false")
	}
}

// TestDiscoverSourceDir_AutoWalkUp — without $C3_SRC_DIR set,
// discoverSourceDir walks up from the test binary's location and
// finds the repo root (since `go test` runs the binary from a
// tempdir inside the repo).
func TestDiscoverSourceDir_AutoWalkUp(t *testing.T) {
	// Save and clear env so the walk-up path is exercised.
	t.Setenv("C3_SRC_DIR", "")
	os.Unsetenv("C3_SRC_DIR")

	dir, ok := discoverSourceDir()
	if !ok {
		// Could legitimately fail in unusual test envs (e.g. running
		// the test binary from outside the source tree, no ~/src/c3).
		// Tolerate that — the production fallback to skip is what
		// matters, not the auto-walk hitting on every system.
		t.Skip("auto-walk didn't find a C3 source tree; non-fatal — production code skips background install in that case")
	}
	if !isC3SourceDir(dir) {
		t.Errorf("discoverSourceDir() returned %q which doesn't pass isC3SourceDir", dir)
	}
}

// TestIsC3SourceDir_RecognizesC3 — the repo root must pass.
func TestIsC3SourceDir_RecognizesC3(t *testing.T) {
	root := findThisRepoRoot(t)
	if !isC3SourceDir(root) {
		t.Errorf("isC3SourceDir(%q) = false; want true", root)
	}
}

// TestIsC3SourceDir_RejectsNonC3 — random tempdir must not pass.
func TestIsC3SourceDir_RejectsNonC3(t *testing.T) {
	dir := t.TempDir()
	if isC3SourceDir(dir) {
		t.Error("isC3SourceDir(tempdir) = true; want false")
	}
	// Now write a go.mod with a different module name.
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module example.com/other\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if isC3SourceDir(dir) {
		t.Error("isC3SourceDir(other module) = true; want false")
	}
}

// TestDefaultInstallRun_SkippedWhenNoSourceDir — set
// $C3_SRC_DIR to a bogus path and confirm defaultInstallRun returns
// skipped=true without touching `go install`. Belt-and-braces against
// the production code accidentally running `go install` from a
// wrong-cwd (which would either fail or, worse, install garbage).
func TestDefaultInstallRun_SkippedWhenNoSourceDir(t *testing.T) {
	t.Setenv("C3_SRC_DIR", "/definitely/does/not/exist/path")
	t.Setenv("HOME", t.TempDir()) // so ~/src/c3 doesn't accidentally exist
	result := defaultInstallRun(context.Background())
	if !result.skipped {
		t.Errorf("defaultInstallRun returned skipped=false, want true (source dir unfindable)")
	}
	if result.err != nil {
		t.Errorf("skipped result has err=%v, want nil", result.err)
	}
}

// TestInstallRunFn_ConcurrentSafety — multiple concurrent
// startBackgroundInstall calls don't race on installRunFn. Belt-
// and-braces — production only calls it once per setup, but TDD
// rule of "test under -race" demands no shared-state races.
func TestInstallRunFn_ConcurrentSafety(t *testing.T) {
	prev := installRunFn
	t.Cleanup(func() { installRunFn = prev })

	var calls atomic.Int32
	installRunFn = func(ctx context.Context) installResult {
		calls.Add(1)
		return installResult{}
	}

	var wg sync.WaitGroup
	chs := make([]<-chan installResult, 10)
	for i := range chs {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			chs[i] = startBackgroundInstall(context.Background())
		}(i)
	}
	wg.Wait()
	for _, ch := range chs {
		<-ch
	}
	if got := calls.Load(); got != 10 {
		t.Errorf("installRunFn called %d times, want 10", got)
	}
}

// findThisRepoRoot walks up from runtime/debug.ReadBuildInfo's
// reported package main path (or fallback CWD) to locate the C3
// repo root. Helper for tests that need a known-good C3 source dir.
func findThisRepoRoot(t *testing.T) string {
	t.Helper()
	// Prefer the executable's location since tests usually run from
	// the package source dir.
	if exe, err := os.Executable(); err == nil {
		if dir := walkUpForC3GoMod(filepath.Dir(exe)); dir != "" {
			return dir
		}
	}
	// Fallback: CWD is the package source dir under `go test`.
	if cwd, err := os.Getwd(); err == nil {
		if dir := walkUpForC3GoMod(cwd); dir != "" {
			return dir
		}
	}
	t.Fatalf("could not locate C3 repo root from test binary; tests need a discoverable go.mod")
	return ""
}
