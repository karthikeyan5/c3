package ipc

import (
	"fmt"
	"strings"
)

// FormatAttached renders an AttachedMsg as the user-facing string the
// agent surfaces verbatim. Shared between the Claude and Codex adapters
// — the broker's proposal cases must render identically on either side
// (maintainer 2026-05-18: "every flow must work the same in Codex"), and
// inlining the formatter twice was the documented source of the
// previously-silent codex parity bug where two proposal actions
// (disambiguate_dm, force_steal) had no codex branch and rendered as
// "attach: unspecified failure".
//
// Wording is the older Claude-adapter form (more explicit; the Codex
// form was a trivial paraphrase that lost no information).
//
// First extracted: 2026-05-19 (audit triage, plan
// `docs/plans/2026-05-19-audit-triage.md`).
func FormatAttached(a *AttachedMsg) string {
	if a.OK {
		s := fmt.Sprintf("attached to %q", a.Name)
		if a.TopicID != nil {
			s += fmt.Sprintf(" (chat %d, thread %d)", a.ChatID, *a.TopicID)
		} else {
			s += fmt.Sprintf(" (chat %d, DM)", a.ChatID)
		}
		return s
	}
	if a.NeedsConfirmation && a.Proposal != nil {
		switch a.Proposal.Action {
		case "create":
			return fmt.Sprintf("No mapping for this directory. I'd create a new topic %q in the %q group. To proceed, call attach(create=true). To use an existing topic instead, call attach(topic_id=<n>).",
				a.Proposal.Name, a.Proposal.Group)
		case "use_existing_other_group":
			alt := ""
			if a.Proposal.Alternative != nil {
				alt = fmt.Sprintf(" or attach(create=true, group=%q) to create a new topic in %q",
					a.Proposal.Alternative.Group, a.Proposal.Alternative.Group)
			}
			return fmt.Sprintf("Found topic %q in group %q (thread %d). Reply yes to claim it%s.",
				a.Proposal.Existing.Name, a.Proposal.Existing.Group, a.Proposal.Existing.TopicID, alt)
		case "disambiguate_dm":
			ex := a.Proposal.Existing
			return fmt.Sprintf("Ambiguous: a topic named %q exists in group %q (thread %d). Did you mean attach to that topic, or to your actual Telegram DM? Confirm by calling attach(topic_id=%d) for the topic, or attach(target=\"dm\", steal=true) for the actual DM.",
				ex.Name, ex.Group, ex.TopicID, ex.TopicID)
		case "force_steal":
			h := a.Proposal.Holder
			return fmt.Sprintf("Topic %q is currently held by %s pid %d (cwd %q). Re-invoke attach with steal=true to evict that session and take the claim. Only do this if the user explicitly confirms.",
				a.Proposal.Name, h.CLI, h.PID, h.CWD)
		}
	}
	// Status-aware structured failures (2026-05-19). Lets the agent tell
	// the user "you need to run setup" vs "your tenant blocked this" vs
	// the generic Err echo. See docs/plans/2026-05-19-codex-policy-3state.md.
	switch a.Status {
	case AttachStatusNoTopicsConfigured:
		return fmt.Sprintf("C3 is not configured for this destination. Run `c3-broker setup` to wire up the Telegram bot token, group chat id, and a starter topic, then retry attach. (broker said: %s)", a.Err)
	case AttachStatusPolicyRejected:
		return fmt.Sprintf("Attach rejected by your CLI host's policy layer. The Telegram destination needs tenant-admin approval before this CLI can attach. Ask the tenant admin to approve the destination, then retry attach. (host said: %s)", a.Err)
	case AttachStatusCwdDefaultCollision:
		// SYMPTOM-3: a bare cwd-default attach resolved to a topic a
		// different live session already holds. Multiple `claude`
		// instances launched from the same parent dir report identical
		// cwds, so the saved mapping is ambiguous. Guide the user toward
		// the likely-correct fix (attach a different topic by name); the
		// "force" path is to re-attach the SAME topic by its explicit
		// name, which triggers the normal force_steal confirmation —
		// there is no `--steal` CLI token (the override is steal=true,
		// handled by the agent on that confirmation).
		holder := "another session"
		if a.Holder != nil {
			holder = fmt.Sprintf("%s pid %d", a.Holder.CLI, a.Holder.PID)
		}
		return fmt.Sprintf(
			"⚠ cwd %s maps to topic %q, already held by %s (a different session).\n"+
				"  Did you mean a different topic? Attach by name:\n"+
				"      /c3:attach <topic>\n"+
				"  Or to take %q from that session, re-attach it by name and confirm the steal:\n"+
				"      /c3:attach %s",
			a.CWD, a.Name, holder, a.Name, a.Name)
	}
	if a.Err != "" {
		return "attach failed: " + a.Err
	}
	return "attach: unspecified failure"
}

// FormatTopics renders a TopicsListMsg as the user-facing string for
// the adapter `topics` MCP tool. Shared between Claude and Codex
// adapters; both inlined byte-identical copies pre-2026-05-19.
//
// Note: the broker CLI `c3-broker topics` (cmd/c3-broker/topics.go) has
// its own slightly-different rendering — no terminating period on the
// empty-list message, slightly different row layout. That divergence
// is intentional (CLI-formatted output vs adapter-formatted output);
// see audit doc 2026-05-18 entry #6.
func FormatTopics(list *TopicsListMsg) string {
	if len(list.Topics) == 0 {
		return "no topics configured."
	}
	lines := make([]string, 0, len(list.Topics)+1)
	lines = append(lines, "known topics:")
	for _, t := range list.Topics {
		state := "free"
		if t.ClaimedBy != nil {
			state = fmt.Sprintf("held by %s pid %d", t.ClaimedBy.CLI, t.ClaimedBy.PID)
		}
		lines = append(lines, fmt.Sprintf("  • %s/%s (chat %d, thread %d) — %s",
			t.Group, t.Name, t.ChatID, t.TopicID, state))
	}
	return strings.Join(lines, "\n")
}
