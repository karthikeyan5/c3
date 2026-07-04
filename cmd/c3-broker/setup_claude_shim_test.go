package main

import (
	"bytes"
	"errors"
	"strings"
	"testing"
)

// TestMaybeInstallClaudeShim_ClaudeHostInvokesInstaller asserts that the
// Claude-host branch of runSetup() (factored out as maybeInstallClaudeShim
// for testability) calls into the shim installer with default args. See
// TODO.md item #17 — under Claude Code, shim install is COMPULSORY at
// setup time per the maintainer's 2026-05-18 call; no prompt, no opt-out.
func TestMaybeInstallClaudeShim_ClaudeHostInvokesInstaller(t *testing.T) {
	called := false
	var gotArgs []string
	prev := installClaudeShimFn
	installClaudeShimFn = func(args []string) error {
		called = true
		gotArgs = args
		return nil
	}
	t.Cleanup(func() { installClaudeShimFn = prev })

	if err := maybeInstallClaudeShim(HostClaude); err != nil {
		t.Fatalf("maybeInstallClaudeShim(HostClaude) returned %v, want nil", err)
	}
	if !called {
		t.Fatal("installer was not invoked under HostClaude")
	}
	// Defaults: empty/nil flag args so runInstallClaudeShim picks
	// ~/.local/bin/claude as the install path and force=false.
	if len(gotArgs) != 0 {
		t.Fatalf("installer args = %v, want empty/nil for defaults", gotArgs)
	}
}

// TestMaybeInstallClaudeShim_CodexHostSkipsInstaller asserts that under
// Codex the installer is NOT invoked — Codex has its own setup path
// (MCP TOML + AGENTS.md) and doesn't use the claude wrapper.
func TestMaybeInstallClaudeShim_CodexHostSkipsInstaller(t *testing.T) {
	called := false
	prev := installClaudeShimFn
	installClaudeShimFn = func(args []string) error {
		called = true
		return nil
	}
	t.Cleanup(func() { installClaudeShimFn = prev })

	if err := maybeInstallClaudeShim(HostCodex); err != nil {
		t.Fatalf("maybeInstallClaudeShim(HostCodex) returned %v, want nil", err)
	}
	if called {
		t.Fatal("installer was invoked under HostCodex; should have been skipped")
	}
}

// TestMaybeInstallClaudeShim_UnknownHostSkipsInstaller covers the
// HostUnknown branch — we don't want a surprise shim install when host
// detection fell back from an explicit override.
func TestMaybeInstallClaudeShim_UnknownHostSkipsInstaller(t *testing.T) {
	called := false
	prev := installClaudeShimFn
	installClaudeShimFn = func(args []string) error {
		called = true
		return nil
	}
	t.Cleanup(func() { installClaudeShimFn = prev })

	if err := maybeInstallClaudeShim(HostUnknown); err != nil {
		t.Fatalf("maybeInstallClaudeShim(HostUnknown) returned %v, want nil", err)
	}
	if called {
		t.Fatal("installer was invoked under HostUnknown; should have been skipped")
	}
}

// TestMaybeInstallClaudeShim_PropagatesInstallerError ensures the helper
// surfaces the installer's error verbatim so runSetup() can log it as a
// warning (non-fatal — setup proceeds, the user just gets a hint to run
// install manually).
func TestMaybeInstallClaudeShim_PropagatesInstallerError(t *testing.T) {
	want := errors.New("simulated: claude-shim launcher not found")
	prev := installClaudeShimFn
	installClaudeShimFn = func(args []string) error {
		return want
	}
	t.Cleanup(func() { installClaudeShimFn = prev })

	got := maybeInstallClaudeShim(HostClaude)
	if !errors.Is(got, want) {
		t.Fatalf("got err %v, want %v", got, want)
	}
}

// TestPrintShimInstallFailure_BlockOnBothStreams asserts that the
// structured failure surface emits the same actionable block on BOTH
// stdout AND stderr (the agent transcript sees stdout; raw shells see
// stderr). The block MUST include:
//   - the named header `[claude shim NOT installed]`
//   - the underlying error message
//   - the actionable next-step command `c3-broker install-claude-shim --force`
//   - an explanation of what --force does
//
// Closes M2 from 2026-05-19 code review.
func TestPrintShimInstallFailure_BlockOnBothStreams(t *testing.T) {
	var stdout, stderr bytes.Buffer
	err := errors.New("simulated: existing non-shim file at ~/.local/bin/claude")
	printShimInstallFailure(&stdout, &stderr, err)

	for _, tc := range []struct {
		name   string
		stream string
	}{
		{"stdout", stdout.String()},
		{"stderr", stderr.String()},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if !strings.Contains(tc.stream, "[claude shim NOT installed]") {
				t.Errorf("%s missing structured header `[claude shim NOT installed]`; got:\n%s", tc.name, tc.stream)
			}
			if !strings.Contains(tc.stream, "simulated: existing non-shim file") {
				t.Errorf("%s missing underlying error text; got:\n%s", tc.name, tc.stream)
			}
			if !strings.Contains(tc.stream, "c3-broker install-claude-shim --force") {
				t.Errorf("%s missing actionable next-step `c3-broker install-claude-shim --force`; got:\n%s", tc.name, tc.stream)
			}
			if !strings.Contains(tc.stream, "--force") || !strings.Contains(tc.stream, "overwrite") {
				t.Errorf("%s does not explain what --force does; got:\n%s", tc.name, tc.stream)
			}
		})
	}
}
