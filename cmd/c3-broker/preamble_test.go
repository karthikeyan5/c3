package main

import (
	"bufio"
	"strings"
	"testing"
)

// TestPreambleCopy_ContainsKeyConcepts is a substring-invariant guard.
// The educational copy MUST mention the load-bearing concepts so a
// fresh user reads about all four: what C3 does (Telegram + CLI), how
// it does voice (STT), why we need a group (topics), and the consent
// gate. If the maintainer rewrites the copy in the morning, this test catches
// accidentally dropping a concept entirely.
//
// Tested against the approved-mode render so the assertion holds in
// both DRAFT and approved states — the load-bearing concepts must
// survive the marker's removal.
func TestPreambleCopy_ContainsKeyConcepts(t *testing.T) {
	required := []string{
		"Telegram",      // what channel we're using
		"bot",           // BotFather setup
		"group",         // group required for topics
		"opic",          // topics, lowercase or capitalized
		"voice",         // voice messages
		"STT",           // mentions STT
		"Claude",        // mentions a host CLI
		"mappings.json", // tells user what config gets written
	}
	approved := renderPreamble(true)
	for _, s := range required {
		if !strings.Contains(approved, s) {
			t.Errorf("renderPreamble(true) missing required substring %q — fresh user won't learn this concept", s)
		}
	}
}

// TestPreambleCopy_NoManualIDHunting — the preamble must not send a fresh
// user hunting for ids (2026-06-30 install feedback #5/#6: pairing codes
// discover the user id and group chat id; @userinfobot and the manual
// negative-chat-id copy are gone). It must instead mention the pairing
// codes so the user knows what's coming.
func TestPreambleCopy_NoManualIDHunting(t *testing.T) {
	out := renderPreamble(true)
	for _, banned := range []string{"userinfobot", "username_to_id_bot", "-100"} {
		if strings.Contains(out, banned) {
			t.Errorf("preamble contains banned id-hunting breadcrumb %q", banned)
		}
	}
	if !strings.Contains(out, "pairing") {
		t.Error("preamble no longer explains the pairing codes — a fresh user won't know how ids get discovered")
	}
}

// TestRenderPreamble_DraftMode_RendersMarker asserts the DRAFT footer
// appears in rendered output whenever draftApproved is false. Acts as
// a forcing function: someone flipping the const without updating the
// copy gets caught here (and vice versa via the approved-mode test).
func TestRenderPreamble_DraftMode_RendersMarker(t *testing.T) {
	out := renderPreamble(false)
	if !strings.Contains(out, "[DRAFT 2026-05-19") {
		t.Error("renderPreamble(false) missing [DRAFT 2026-05-19 footer — DRAFT mode must show the marker")
	}
	if !strings.Contains(out, "pending maintainer review") {
		t.Error("renderPreamble(false) missing `pending maintainer review` text — the DRAFT footer must say what's pending")
	}
}

// TestRenderPreamble_ApprovedMode_OmitsMarker asserts the DRAFT footer
// is gone in approved mode. Without this test, flipping draftApproved
// to true while accidentally leaving the marker string embedded in
// preambleBody would still ship a DRAFT-marked render to users.
func TestRenderPreamble_ApprovedMode_OmitsMarker(t *testing.T) {
	out := renderPreamble(true)
	if strings.Contains(out, "DRAFT") {
		t.Errorf("renderPreamble(true) contains DRAFT text — approved mode must NOT show the marker; rendered output:\n%s", out)
	}
	if strings.Contains(out, "pending maintainer review") {
		t.Error("renderPreamble(true) contains `pending maintainer review` — approved mode must not show pending-review text")
	}
}

// TestPreambleCopy_NotTooLong is a soft cap: the maintainer explicitly said
// "don't make it lengthy paragraphs". 80 lines is well under what a
// typical terminal can show; 200 lines is screen-scroll territory. We
// alarm at the upper bound — if the copy crosses 120 lines, something
// has gone wrong.
func TestPreambleCopy_NotTooLong(t *testing.T) {
	lines := strings.Count(renderPreamble(draftApproved), "\n")
	if lines > 120 {
		t.Errorf("renderPreamble is %d lines — maintainer rejects lengthy paragraphs; tighten before shipping", lines)
	}
}

// TestConfirmInstall_YesProceeds — "y" answer returns true.
func TestConfirmInstall_YesProceeds(t *testing.T) {
	t.Setenv("C3_NO_PROMPT", "")
	r := bufio.NewReader(strings.NewReader("y\n"))
	if !confirmInstall(r) {
		t.Error("confirmInstall(\"y\") = false, want true")
	}
}

// TestConfirmInstall_NoAborts — "n" answer returns false. Critical
// negative path: caller must respect refusal and not run install.
func TestConfirmInstall_NoAborts(t *testing.T) {
	t.Setenv("C3_NO_PROMPT", "")
	r := bufio.NewReader(strings.NewReader("n\n"))
	if confirmInstall(r) {
		t.Error("confirmInstall(\"n\") = true, want false")
	}
}

// TestConfirmInstall_NoVariantsAlsoAbort — "no", "No", "NO" all
// abort. Anything else is a yes (so the user is biased toward
// proceeding after reading the preamble).
func TestConfirmInstall_NoVariantsAlsoAbort(t *testing.T) {
	for _, in := range []string{"n\n", "no\n", "No\n", "NO\n", "  no  \n"} {
		t.Run(strings.TrimSpace(in), func(t *testing.T) {
			t.Setenv("C3_NO_PROMPT", "")
			r := bufio.NewReader(strings.NewReader(in))
			if confirmInstall(r) {
				t.Errorf("confirmInstall(%q) = true, want false", in)
			}
		})
	}
}

// TestConfirmInstall_EmptyDefaultsToYes — pressing enter without
// typing anything proceeds. Most fresh installers will hit enter
// after reading the preamble; we treat that as consent.
func TestConfirmInstall_EmptyDefaultsToYes(t *testing.T) {
	t.Setenv("C3_NO_PROMPT", "")
	r := bufio.NewReader(strings.NewReader("\n"))
	if !confirmInstall(r) {
		t.Error("confirmInstall(\"\") = false, want true (default-yes)")
	}
}

// TestConfirmInstall_NoPromptEnvBypasses — C3_NO_PROMPT=1 skips stdin
// entirely. This is the non-interactive path Codex uses when it
// drives setup programmatically.
func TestConfirmInstall_NoPromptEnvBypasses(t *testing.T) {
	for _, v := range []string{"1", "true", "yes", "TRUE", "Yes"} {
		t.Run(v, func(t *testing.T) {
			t.Setenv("C3_NO_PROMPT", v)
			// Reader returns immediate EOF — if confirmInstall reads
			// stdin, it would see empty and (currently) default-yes
			// anyway, but the contract is "must not read stdin", so
			// a deliberate marker reader catches the regression.
			r := bufio.NewReader(strings.NewReader("n\n")) // would say NO if read
			if !confirmInstall(r) {
				t.Errorf("C3_NO_PROMPT=%q didn't bypass stdin — got false from a reader that says \"n\"", v)
			}
			// And it MUST not have consumed the stdin line either.
			rest, _ := r.ReadString('\n')
			if rest != "n\n" {
				t.Errorf("C3_NO_PROMPT=%q consumed stdin (got %q) — should leave it untouched for downstream prompts", v, rest)
			}
		})
	}
}

// TestConfirmInstall_NoPromptUnsetEmpty — unset, empty, "0", "false"
// all keep the prompt enabled (normal interactive path).
func TestConfirmInstall_NoPromptUnsetEmpty(t *testing.T) {
	for _, v := range []string{"", "0", "false", "no", "garbage"} {
		t.Run("v="+v, func(t *testing.T) {
			t.Setenv("C3_NO_PROMPT", v)
			r := bufio.NewReader(strings.NewReader("n\n"))
			if confirmInstall(r) {
				t.Errorf("C3_NO_PROMPT=%q should not bypass; got true on \"n\" stdin", v)
			}
		})
	}
}
