package ipc

import (
	"strings"
	"testing"
)

// TestFormatAttached_OKWithTopic verifies the OK-with-thread shape used
// when the broker auto-claims (or the agent attaches to) a real topic.
func TestFormatAttached_OKWithTopic(t *testing.T) {
	tid := int64(914)
	msg := &AttachedMsg{
		OK: true, Name: "my-feature", ChatID: -100123, TopicID: &tid,
	}
	got := FormatAttached(msg)
	for _, w := range []string{`attached to "my-feature"`, "chat -100123", "thread 914"} {
		if !strings.Contains(got, w) {
			t.Errorf("FormatAttached(ok+topic) missing %q in %q", w, got)
		}
	}
}

// TestFormatAttached_OKDM verifies the OK-DM shape (no TopicID).
func TestFormatAttached_OKDM(t *testing.T) {
	msg := &AttachedMsg{OK: true, Name: "dm", ChatID: 12345678}
	got := FormatAttached(msg)
	if !strings.Contains(got, "DM") {
		t.Errorf("FormatAttached(ok+dm) missing %q in %q", "DM", got)
	}
}

// TestFormatAttached_ProposalParity confirms every proposal action the
// broker can emit is rendered with actionable user-facing text — no
// "unspecified failure" leakage. maintainer 2026-05-18: "I absolutely need
// the flow and the same flow to work in Codex." Both adapters used to
// implement this in parallel and the codex copy silently dropped two
// branches; centralising removes that drift surface forever.
func TestFormatAttached_ProposalParity(t *testing.T) {
	cases := []struct {
		name    string
		msg     AttachedMsg
		want    []string
		wantNot []string
	}{
		{
			name: "create",
			msg: AttachedMsg{
				NeedsConfirmation: true,
				Proposal: &Proposal{
					Action: "create", Name: "my-feature", Group: "dev",
				},
			},
			// The confirm command must carry the name explicitly — a bare
			// attach(create=true) no longer works (cwd-basename backfill deleted,
			// spec §2). The name is inside attach(name="…", create=true).
			want:    []string{`attach(name="my-feature", create=true)`, "attach(topic_id=<n>)"},
			wantNot: []string{"call attach(create=true)"},
		},
		{
			name: "use_existing_other_group",
			msg: AttachedMsg{
				NeedsConfirmation: true,
				Proposal: &Proposal{
					Action:   "use_existing_other_group",
					Existing: &TopicEntry{Name: "my-feature", Group: "other", TopicID: 42},
				},
			},
			want: []string{`Found topic "my-feature" in group "other"`, "Reply yes"},
		},
		{
			name: "use_existing_other_group_with_alternative",
			msg: AttachedMsg{
				NeedsConfirmation: true,
				Proposal: &Proposal{
					Action:      "use_existing_other_group",
					Existing:    &TopicEntry{Name: "my-feature", Group: "other", TopicID: 42},
					Alternative: &Proposal{Group: "dev"},
				},
			},
			want: []string{`attach(create=true, group="dev")`},
		},
		{
			name: "disambiguate_dm",
			msg: AttachedMsg{
				NeedsConfirmation: true,
				Proposal: &Proposal{
					Action:   "disambiguate_dm",
					Existing: &TopicEntry{Name: "dm", Group: "dev", TopicID: 7},
				},
			},
			want:    []string{"Ambiguous", "attach(topic_id=7)", `attach(target="dm", steal=true)`},
			wantNot: []string{"unspecified failure"},
		},
		{
			name: "force_steal",
			msg: AttachedMsg{
				NeedsConfirmation: true,
				Proposal: &Proposal{
					Action: "force_steal", Name: "tg-mux",
					Holder: &Holder{CLI: "claude", PID: 1234, CWD: "/home/x"},
				},
			},
			want:    []string{"steal=true", "claude pid 1234"},
			wantNot: []string{"unspecified failure"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := FormatAttached(&tc.msg)
			for _, w := range tc.want {
				if !strings.Contains(got, w) {
					t.Errorf("FormatAttached(%s) = %q\nwant contains %q", tc.name, got, w)
				}
			}
			for _, w := range tc.wantNot {
				if strings.Contains(got, w) {
					t.Errorf("FormatAttached(%s) = %q\nshould NOT contain %q", tc.name, got, w)
				}
			}
		})
	}
}

// TestFormatAttached_NoTopicsConfigured asserts the 3-state actionable
// guidance for the broker's no_topics_configured status.
func TestFormatAttached_NoTopicsConfigured(t *testing.T) {
	msg := &AttachedMsg{
		Op:     OpAttached,
		OK:     false,
		Status: AttachStatusNoTopicsConfigured,
		Err:    "attach dm: channels.telegram.dm_chat_id not set in mappings.json",
	}
	got := FormatAttached(msg)
	for _, w := range []string{"not configured", "c3-broker setup"} {
		if !strings.Contains(got, w) {
			t.Errorf("FormatAttached(no_topics_configured) missing %q in %q", w, got)
		}
	}
}

// TestFormatAttached_PolicyRejected asserts the 3-state actionable
// guidance for the policy_rejected status (Codex tenant policy layer).
func TestFormatAttached_PolicyRejected(t *testing.T) {
	msg := &AttachedMsg{
		Op:     OpAttached,
		OK:     false,
		Status: AttachStatusPolicyRejected,
		Err:    "host rejected",
	}
	got := FormatAttached(msg)
	for _, w := range []string{"policy layer", "tenant admin", "approve", "retry attach"} {
		if !strings.Contains(got, w) {
			t.Errorf("FormatAttached(policy_rejected) missing %q in %q", w, got)
		}
	}
}

// TestFormatAttached_CwdDefaultCollision asserts the guided message the
// broker emits when a BARE (cwd-default) attach resolves to a topic that
// is already held by a different live session. The rendered text must
// name the cwd, the resolved topic, the holder (cli+pid), and offer both
// the attach-by-name path and the steal (re-attach-by-name + confirm)
// escape hatch. SYMPTOM-3 fix.
func TestFormatAttached_CwdDefaultCollision(t *testing.T) {
	tid := int64(281)
	msg := &AttachedMsg{
		Op:      OpAttached,
		OK:      false,
		Status:  AttachStatusCwdDefaultCollision,
		Name:    "c3",
		CWD:     "/home/user/projects",
		ChatID:  -100,
		TopicID: &tid,
		Holder:  &Holder{CLI: "claude", PID: 9823, CWD: "/home/user/projects"},
	}
	got := FormatAttached(msg)
	for _, w := range []string{
		"/home/user/projects", // the colliding cwd
		"c3",                  // the resolved topic name
		"claude",              // holder cli
		"9823",                // holder pid
		"attach",              // generic guidance verb
		"steal",               // force escape hatch (re-attach by name + confirm)
	} {
		if !strings.Contains(got, w) {
			t.Errorf("FormatAttached(cwd_default_collision) missing %q in %q", w, got)
		}
	}
	if strings.Contains(got, "unspecified failure") {
		t.Errorf("FormatAttached(cwd_default_collision) leaked unspecified-failure: %q", got)
	}
}

// TestFormatAttached_ErrFallback confirms a bare Err (no Status, no
// Proposal) renders as "attach failed: <err>".
func TestFormatAttached_ErrFallback(t *testing.T) {
	msg := &AttachedMsg{OK: false, Err: "broker exploded"}
	got := FormatAttached(msg)
	if !strings.Contains(got, "attach failed: broker exploded") {
		t.Errorf("FormatAttached(err) = %q", got)
	}
}

// TestFormatAttached_UnspecifiedFailure is the final fallback when no
// signal is set on the message.
func TestFormatAttached_UnspecifiedFailure(t *testing.T) {
	msg := &AttachedMsg{OK: false}
	got := FormatAttached(msg)
	if !strings.Contains(got, "unspecified failure") {
		t.Errorf("FormatAttached(empty) = %q", got)
	}
}

// TestFormatTopics_Empty asserts the no-topics path matches the existing
// adapter contract — "no topics configured." (with period).
func TestFormatTopics_Empty(t *testing.T) {
	got := FormatTopics(&TopicsListMsg{})
	if got != "no topics configured." {
		t.Errorf("FormatTopics(empty) = %q; want %q", got, "no topics configured.")
	}
}

// TestFormatTopics_Free renders an unclaimed topic.
func TestFormatTopics_Free(t *testing.T) {
	msg := &TopicsListMsg{Topics: []TopicEntry{
		{Group: "dev", Name: "feat", ChatID: -100, TopicID: 1},
	}}
	got := FormatTopics(msg)
	for _, w := range []string{"known topics:", "dev/feat", "chat -100", "thread 1", "free"} {
		if !strings.Contains(got, w) {
			t.Errorf("FormatTopics(free) missing %q in %q", w, got)
		}
	}
}

// TestFormatTopics_Held renders a claimed topic with holder details.
func TestFormatTopics_Held(t *testing.T) {
	msg := &TopicsListMsg{Topics: []TopicEntry{
		{
			Group: "dev", Name: "feat", ChatID: -100, TopicID: 1,
			ClaimedBy: &Holder{CLI: "claude", PID: 999},
		},
	}}
	got := FormatTopics(msg)
	if !strings.Contains(got, "held by claude pid 999") {
		t.Errorf("FormatTopics(held) = %q", got)
	}
}
