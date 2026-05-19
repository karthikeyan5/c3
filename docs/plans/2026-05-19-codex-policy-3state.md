# Codex Policy 3-State Attach Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Disambiguate three distinct attach-failure modes (no_topics_configured / policy_rejected / ok) so the calling agent can tell the user the right next-step (setup vs. tenant approval vs. success).

**Architecture:** Add a typed `Status` field (string enum) to `ipc.AttachedMsg` populated by the broker for the not-OK branches. Broker can directly detect `no_topics_configured` (mappings empty). `policy_rejected` is a wire-shape that broker NEVER emits on its own — it's reserved for the adapter to surface when a downstream signal (only available to the Codex adapter's caller, not the broker or the adapter) indicates the Codex host's policy layer pre-rejected the call. The Codex adapter exposes an opt-in `policy_rejected` argument the agent can pass on a re-invoke after observing rejection in the Codex UI. Existing proposal-flow branches (`create` / `disambiguate_dm` / `force_steal` / `use_existing_other_group`) are untouched.

**Tech Stack:** Go 1.x. `internal/ipc` (wire shapes), `internal/broker` (attach flow), `cmd/c3-claude-adapter` & `cmd/c3-codex-adapter` (formatters), `DEBUGGING.md` (operator docs).

---

## Phase 0 — Investigation findings (PRE-COMMIT to design)

This phase was completed during planning. Findings inform the design choices in Phase 1+.

**Q: Can the Codex adapter preflight `approvals_reviewer` mode?**

A: **No.** Investigation results:

1. `approvals_reviewer = "auto_review" | "guardian_subagent"` and `mcp_servers.<name>.tools.<tool>.approval_mode` live in `~/.codex/config.toml` — owned by Codex, not exposed via env or the MCP protocol initialize handshake to spawned MCP servers.
2. The MCP go-sdk `InitializeResult` / `ServerInfo` payloads don't surface host policy state.
3. When the Codex host pre-rejects an MCP tool call as "unacceptable risk", the spawned adapter **never receives the call** — the rejection happens upstream in the host before forwarding. The adapter cannot observe a request → cannot translate a non-existent failure.
4. The agent (LLM driving Codex) CAN observe the rejection: it sees `tool call rejected` / "unacceptable risk" surface in its turn output. So the only practical surface is for the agent to re-invoke `attach` with an explicit `policy_rejected=true` hint so we can format a clear user-facing message.
5. Parsing `~/.codex/config.toml` from the adapter is rejected: brittle (Codex owns the file format), it's also session-state-dependent (per-project trust_level, tenant overrides), and even reading "approval_mode=approve" doesn't tell us a specific request was rejected — only the agent's observation does.

**Conclusion:** Build the wire shape that ALLOWS `policy_rejected` to be communicated, but emit it from the adapter only when the agent passes the hint. Broker emits `no_topics_configured` directly. Document the workflow.

**Sakthi's pilot symptom** (broker says success, then immediate notification dispatch fails) is a separate concern — it's a delivery-side issue (failed `deliver FAIL` log), not an attach-side issue. The broker DOES already log that. Out of scope for this plan; tracked elsewhere if it recurs.

---

## File Structure

- **Modify**: `internal/ipc/messages.go` — add `Status` field on `AttachedMsg` + named constants for the three statuses.
- **Modify**: `internal/broker/attach.go` — emit `Status="no_topics_configured"` when mappings has zero channels OR the resolved channel has zero topics AND zero DM. Set `Status="ok"` on success branches.
- **Modify**: `internal/ipc/messages.go` — extend `AttachReq` with optional `PolicyRejected bool` hint that the Codex adapter sets when the agent invokes with `policy_rejected=true`.
- **Modify**: `internal/broker/attach.go` — branch on `AttachReq.PolicyRejected` BEFORE any other handling and short-circuit with `Status="policy_rejected"` response.
- **Modify**: `cmd/c3-claude-adapter/main.go` — extend `formatAttached` to render the new statuses with distinct user-facing text.
- **Modify**: `cmd/c3-codex-adapter/main.go` — extend `formatAttached` to render the new statuses; accept a `policy_rejected` bool tool argument that the agent can pass on re-invoke.
- **Modify**: `DEBUGGING.md` — add "Codex policy layer rejected attach" section.
- **Tests**: extend `internal/broker/attach_test.go`, `cmd/c3-claude-adapter/wire_test.go` (if needed for formatter coverage), and a new `cmd/c3-codex-adapter/wire_test.go` formatter test.

---

## Task 1: Wire shape — add Status enum and PolicyRejected hint

**Files:**
- Modify: `internal/ipc/messages.go`
- Test: `internal/ipc/messages_test.go`

- [ ] **Step 1: Write the failing test for Status constant exposure**

Add to `internal/ipc/messages_test.go` (new file if not present, else append):

```go
package ipc

import (
	"encoding/json"
	"testing"
)

func TestAttachStatusConstants(t *testing.T) {
	// Wire-shape lock: these strings show up in adapter formatters and
	// agent prompts. Renaming them is a breaking change.
	cases := map[AttachStatus]string{
		AttachStatusOK:                 "ok",
		AttachStatusNoTopicsConfigured: "no_topics_configured",
		AttachStatusPolicyRejected:     "policy_rejected",
	}
	for got, want := range cases {
		if string(got) != want {
			t.Errorf("AttachStatus constant: got %q want %q", string(got), want)
		}
	}
}

func TestAttachedMsg_StatusFieldOmitEmpty(t *testing.T) {
	// Status is wire-additive: a message without Status (legacy / pre-3state
	// proposal flows) must serialize without the key, so any consumer doing
	// a strict re-deserialize round-trips byte-equal.
	msg := AttachedMsg{Op: OpAttached, OK: true}
	raw, err := json.Marshal(msg)
	if err != nil {
		t.Fatal(err)
	}
	if got := string(raw); got == "" || containsField(got, "status") {
		t.Errorf("AttachedMsg with empty Status must omit status field; got %q", got)
	}
}

func TestAttachReq_PolicyRejectedFieldOmitEmpty(t *testing.T) {
	req := AttachReq{Op: OpAttach}
	raw, err := json.Marshal(req)
	if err != nil {
		t.Fatal(err)
	}
	if got := string(raw); containsField(got, "policy_rejected") {
		t.Errorf("AttachReq with PolicyRejected=false must omit field; got %q", got)
	}
}

func containsField(j, k string) bool {
	for i := 0; i+len(k)+2 < len(j); i++ {
		if j[i] == '"' && j[i+1:i+1+len(k)] == k && j[i+1+len(k)] == '"' {
			return true
		}
	}
	return false
}
```

- [ ] **Step 2: Run test to verify failures**

Run: `go test ./internal/ipc/ -run TestAttachStatus -count=1 -v && go test ./internal/ipc/ -run TestAttachedMsg_StatusFieldOmitEmpty -count=1 -v && go test ./internal/ipc/ -run TestAttachReq_PolicyRejectedFieldOmitEmpty -count=1 -v`
Expected: FAIL with "undefined: AttachStatus" / "AttachedMsg has no field Status" / "AttachReq has no field PolicyRejected".

- [ ] **Step 3: Add the constants and fields**

Edit `internal/ipc/messages.go`:

1. Above the `AttachReq` struct, add:

```go
// AttachStatus is a typed enum describing the outcome of an attach IPC
// op. Added 2026-05-19 so calling agents can distinguish "user must
// configure C3" (no_topics_configured) from "Codex policy layer rejected
// the call before it reached the adapter" (policy_rejected) from "success"
// (ok). Pre-2026-05-19 messages omit the field; consumers treat absence
// as "interpret OK/Err/Proposal as before."
type AttachStatus string

const (
	// AttachStatusOK indicates the attach succeeded; topic + welcome flow.
	AttachStatusOK AttachStatus = "ok"

	// AttachStatusNoTopicsConfigured indicates the broker's mappings has
	// no channels / DM / topics — the user hasn't run `c3-broker setup`
	// yet. Emitted by the broker directly.
	AttachStatusNoTopicsConfigured AttachStatus = "no_topics_configured"

	// AttachStatusPolicyRejected indicates the CLI host's policy layer
	// rejected the call. ONLY ever set in response to AttachReq.PolicyRejected;
	// the broker can't detect the underlying signal (it lives upstream of
	// the adapter in the CLI host). The Codex adapter exposes a
	// `policy_rejected` tool argument the agent passes on a re-invoke
	// after observing the rejection in the Codex UI.
	AttachStatusPolicyRejected AttachStatus = "policy_rejected"
)
```

2. In `AttachReq`, append:

```go
	// PolicyRejected: hint set true by the calling agent on a re-invoke
	// after observing the host's policy layer reject a prior attach (e.g.
	// Codex's approvals_reviewer="auto_review" surfaces "unacceptable
	// risk rejection"). The broker treats this as a pure surface-state
	// request: it short-circuits with AttachStatusPolicyRejected so the
	// adapter formatter can render the actionable next-step (ask tenant
	// admin to approve the Telegram destination, then retry).
	PolicyRejected bool `json:"policy_rejected,omitempty"`
```

3. In `AttachedMsg`, append:

```go
	// Status disambiguates the outcome. See AttachStatus godoc. Omitted
	// for backward compat with pre-2026-05-19 consumers that switch on
	// OK / NeedsConfirmation / Err.
	Status AttachStatus `json:"status,omitempty"`
```

- [ ] **Step 4: Run tests to verify pass**

Run: `go test ./internal/ipc/ -count=1`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/ipc/messages.go internal/ipc/messages_test.go
git commit -m "ipc: add AttachStatus enum + PolicyRejected hint for 3-state attach"
```

---

## Task 2: Broker emits Status=ok on every success path

**Files:**
- Modify: `internal/broker/attach.go`
- Test: `internal/broker/attach_test.go`

- [ ] **Step 1: Write the failing test**

Append to `internal/broker/attach_test.go`:

```go
func TestAttach_DM_EmitsStatusOK(t *testing.T) {
	mf := mfWithTelegram()
	fc := &fakeChannel{}
	b := brokerWithChannel(t, mf, fc)
	defer b.Shutdown()

	peer, done := peerPair(t, b)
	defer done()
	helloAck(t, peer, "/x")

	_ = peer.WriteJSON(ipc.AttachReq{Op: ipc.OpAttach, Target: "dm"})
	raw, _ := peer.ReadFrame()
	var ack ipc.AttachedMsg
	_ = json.Unmarshal(raw, &ack)

	if ack.Status != ipc.AttachStatusOK {
		t.Errorf("Status=%q want %q", ack.Status, ipc.AttachStatusOK)
	}
}

func TestAttach_ByName_EmitsStatusOK(t *testing.T) {
	mf := mfWithTelegram()
	fc := &fakeChannel{}
	b := brokerWithChannel(t, mf, fc)
	defer b.Shutdown()

	peer, done := peerPair(t, b)
	defer done()
	helloAck(t, peer, "/projects/c3")

	_ = peer.WriteJSON(ipc.AttachReq{Op: ipc.OpAttach, CWD: "/projects/c3", Name: "c3"})
	raw, _ := peer.ReadFrame()
	var ack ipc.AttachedMsg
	_ = json.Unmarshal(raw, &ack)

	if ack.Status != ipc.AttachStatusOK {
		t.Errorf("Status=%q want %q", ack.Status, ipc.AttachStatusOK)
	}
}

func TestAttach_ByTopicID_EmitsStatusOK(t *testing.T) {
	mf := mfWithTelegram()
	fc := &fakeChannel{}
	b := brokerWithChannel(t, mf, fc)
	defer b.Shutdown()

	peer, done := peerPair(t, b)
	defer done()
	helloAck(t, peer, "/x")

	tid := int64(999)
	_ = peer.WriteJSON(ipc.AttachReq{Op: ipc.OpAttach, CWD: "/x", TopicID: &tid})
	raw, _ := peer.ReadFrame()
	var ack ipc.AttachedMsg
	_ = json.Unmarshal(raw, &ack)

	if ack.Status != ipc.AttachStatusOK {
		t.Errorf("Status=%q want %q", ack.Status, ipc.AttachStatusOK)
	}
}

func TestAttach_CreateTrue_EmitsStatusOK(t *testing.T) {
	mf := mfWithTelegram()
	fc := &fakeChannel{createReturnID: 917}
	b := brokerWithChannel(t, mf, fc)
	defer b.Shutdown()

	peer, done := peerPair(t, b)
	defer done()
	helloAck(t, peer, "/projects/widget-foo")

	_ = peer.WriteJSON(ipc.AttachReq{
		Op: ipc.OpAttach, CWD: "/projects/widget-foo",
		Name: "widget-foo", Create: true,
	})
	raw, _ := peer.ReadFrame()
	var ack ipc.AttachedMsg
	_ = json.Unmarshal(raw, &ack)

	if ack.Status != ipc.AttachStatusOK {
		t.Errorf("Status=%q want %q", ack.Status, ipc.AttachStatusOK)
	}
}
```

- [ ] **Step 2: Run tests to verify failures**

Run: `go test ./internal/broker/ -run 'TestAttach_(DM|ByName|ByTopicID|CreateTrue)_EmitsStatusOK' -count=1 -v`
Expected: FAIL — Status is unset on the AttachedMsg success replies.

- [ ] **Step 3: Set Status on all four success-path WriteJSON calls**

Edit `internal/broker/attach.go`. For each of these four AttachedMsg writes, add `Status: ipc.AttachStatusOK`:

In `attachDM` (around line 179-185):

```go
	_ = conn.WriteJSON(ipc.AttachedMsg{
		Op:      ipc.OpAttached,
		OK:      true,
		Status:  ipc.AttachStatusOK,
		Channel: chanName,
		ChatID:  cc.DMChatID,
		Name:    "dm",
	})
```

In `attachByTopicID` (around line 242-250):

```go
	_ = conn.WriteJSON(ipc.AttachedMsg{
		Op:      ipc.OpAttached,
		OK:      true,
		Status:  ipc.AttachStatusOK,
		Channel: chanName,
		ChatID:  gCfg.ChatID,
		TopicID: &tid,
		Name:    tp.Name,
		Group:   gName,
	})
```

In `attachByName` saved-mapping branch (around line 297-302):

```go
				_ = conn.WriteJSON(ipc.AttachedMsg{
					Op: ipc.OpAttached, OK: true,
					Status:  ipc.AttachStatusOK,
					Channel: chanName, ChatID: m.ChatID, TopicID: tidPtr,
					Name: m.Name, Group: m.Group,
				})
```

In `attachByName` default-group-hit branch (around line 335-340):

```go
		_ = conn.WriteJSON(ipc.AttachedMsg{
			Op: ipc.OpAttached, OK: true,
			Status:  ipc.AttachStatusOK,
			Channel: chanName, ChatID: tp.ChatID, TopicID: &tid,
			Name: tp.Name, Group: tp.Group,
		})
```

In `createAndClaim` (around line 416-420):

```go
	_ = conn.WriteJSON(ipc.AttachedMsg{
		Op: ipc.OpAttached, OK: true,
		Status:  ipc.AttachStatusOK,
		Channel: chanName, ChatID: chatID, TopicID: &tid,
		Name: name, Group: gName,
	})
```

- [ ] **Step 4: Run tests to verify pass**

Run: `go test ./internal/broker/ -run 'TestAttach_(DM|ByName|ByTopicID|CreateTrue)_EmitsStatusOK' -count=1 -v`
Expected: PASS.

- [ ] **Step 5: Run full broker tests to verify nothing else broke**

Run: `go test ./internal/broker/ -count=1`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add internal/broker/attach.go internal/broker/attach_test.go
git commit -m "broker: emit Status=ok on every attach success path"
```

---

## Task 3: Broker emits Status=no_topics_configured when mappings empty

**Files:**
- Modify: `internal/broker/attach.go`
- Test: `internal/broker/attach_test.go`

**Decision:** "no_topics_configured" fires when the resolved channel has BOTH:
- `DMChatID == 0` (no DM destination configured), AND
- `len(Topics) == 0` (no topics registered)

Note: the existing "no channel registered" branch (line 49-55) already errors with `Err=...` — that's a separate failure mode (no channels at all). We extend that branch AND add the new check below it to cover the case where channels exist but the active one has no destinations.

- [ ] **Step 1: Write the failing tests**

Append to `internal/broker/attach_test.go`:

```go
func TestAttach_NoChannelsConfigured_EmitsStatusNoTopicsConfigured(t *testing.T) {
	mf := &mappings.MappingsFile{
		SchemaVersion: 1,
		Channels:      map[string]mappings.ChannelConfig{},
		Mappings:      map[string]mappings.Mapping{},
	}
	fc := &fakeChannel{}
	b := brokerWithChannel(t, mf, fc)
	defer b.Shutdown()

	peer, done := peerPair(t, b)
	defer done()
	helloAck(t, peer, "/x")

	_ = peer.WriteJSON(ipc.AttachReq{Op: ipc.OpAttach, Target: "dm"})
	raw, _ := peer.ReadFrame()
	var ack ipc.AttachedMsg
	_ = json.Unmarshal(raw, &ack)

	if ack.OK {
		t.Error("attach with no channels should fail")
	}
	if ack.Status != ipc.AttachStatusNoTopicsConfigured {
		t.Errorf("Status=%q want %q", ack.Status, ipc.AttachStatusNoTopicsConfigured)
	}
}

func TestAttach_ChannelWithoutDMOrTopics_EmitsStatusNoTopicsConfigured(t *testing.T) {
	mf := &mappings.MappingsFile{
		SchemaVersion: 1,
		Channels: map[string]mappings.ChannelConfig{
			"telegram": {
				DefaultGroup: "main",
				Groups: map[string]mappings.GroupConfig{
					"main": {ChatID: -100},
				},
				DMChatID: 0,
				Topics:   nil,
			},
		},
		Mappings: map[string]mappings.Mapping{},
	}
	fc := &fakeChannel{}
	b := brokerWithChannel(t, mf, fc)
	defer b.Shutdown()

	peer, done := peerPair(t, b)
	defer done()
	helloAck(t, peer, "/x")

	_ = peer.WriteJSON(ipc.AttachReq{Op: ipc.OpAttach, Target: "dm"})
	raw, _ := peer.ReadFrame()
	var ack ipc.AttachedMsg
	_ = json.Unmarshal(raw, &ack)

	if ack.OK {
		t.Error("attach with channel-but-no-destinations should fail")
	}
	if ack.Status != ipc.AttachStatusNoTopicsConfigured {
		t.Errorf("Status=%q want %q (DM=0, Topics=nil should be 'no destinations configured')",
			ack.Status, ipc.AttachStatusNoTopicsConfigured)
	}
}
```

- [ ] **Step 2: Run tests to verify failures**

Run: `go test ./internal/broker/ -run 'TestAttach_(NoChannelsConfigured|ChannelWithoutDMOrTopics)' -count=1 -v`
Expected: FAIL — Status field unset.

- [ ] **Step 3: Update broker to set Status in the no-config branches**

Edit `internal/broker/attach.go`:

In `handleAttach`, change the existing "no channel registered" block:

```go
	if chanName == "" {
		_ = conn.WriteJSON(ipc.AttachedMsg{
			Op: ipc.OpAttached, OK: false,
			Status: ipc.AttachStatusNoTopicsConfigured,
			Err:    "no channel registered; configure mappings.json:channels.<name>",
		})
		return
	}
```

In `attachDM`, change the "DMChatID not set" block to detect the broader no-destinations case. Replace:

```go
	cc, ok := b.Mappings().Channels[chanName]
	if !ok || cc.DMChatID == 0 {
		_ = conn.WriteJSON(ipc.AttachedMsg{
			Op: ipc.OpAttached, OK: false,
			Err: fmt.Sprintf("attach dm: channels.%s.dm_chat_id not set in mappings.json", chanName),
		})
		return
	}
```

with:

```go
	cc, ok := b.Mappings().Channels[chanName]
	if !ok {
		_ = conn.WriteJSON(ipc.AttachedMsg{
			Op: ipc.OpAttached, OK: false,
			Status: ipc.AttachStatusNoTopicsConfigured,
			Err:    fmt.Sprintf("attach: channel %q not in mappings.json", chanName),
		})
		return
	}
	if cc.DMChatID == 0 {
		// DM specifically not set. If topics are ALSO empty, surface as
		// no_topics_configured (full setup missing); otherwise it's a
		// targeted "DM not configured but topics exist" — still emit
		// the structured status because the user lacks a DM destination.
		status := ipc.AttachStatusNoTopicsConfigured
		if len(cc.Topics) > 0 {
			// Topics exist; this is a partial-config case where DM
			// alone is missing. Keep the structured status for the
			// formatter, but the message is more specific.
			status = ipc.AttachStatusNoTopicsConfigured
		}
		_ = conn.WriteJSON(ipc.AttachedMsg{
			Op: ipc.OpAttached, OK: false,
			Status: status,
			Err:    fmt.Sprintf("attach dm: channels.%s.dm_chat_id not set in mappings.json", chanName),
		})
		return
	}
```

- [ ] **Step 4: Run tests to verify pass**

Run: `go test ./internal/broker/ -run 'TestAttach_(NoChannelsConfigured|ChannelWithoutDMOrTopics)' -count=1 -v`
Expected: PASS.

- [ ] **Step 5: Run full broker tests**

Run: `go test ./internal/broker/ -count=1`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add internal/broker/attach.go internal/broker/attach_test.go
git commit -m "broker: emit Status=no_topics_configured for unconfigured destinations"
```

---

## Task 4: Broker emits Status=policy_rejected when AttachReq.PolicyRejected is set

**Files:**
- Modify: `internal/broker/attach.go`
- Test: `internal/broker/attach_test.go`

- [ ] **Step 1: Write the failing test**

Append to `internal/broker/attach_test.go`:

```go
func TestAttach_PolicyRejectedHint_ShortCircuitsToStatusPolicyRejected(t *testing.T) {
	// Codex adapter sets PolicyRejected=true on a re-invoke after the
	// agent observed the host's policy layer reject the prior attach.
	// Broker MUST short-circuit: don't validate, don't claim, don't
	// register topics. Just surface the structured status so the
	// adapter formatter renders the actionable user message.
	mf := mfWithTelegram()
	fc := &fakeChannel{}
	b := brokerWithChannel(t, mf, fc)
	defer b.Shutdown()

	peer, done := peerPair(t, b)
	defer done()
	helloAck(t, peer, "/x")

	_ = peer.WriteJSON(ipc.AttachReq{
		Op: ipc.OpAttach, CWD: "/x", Name: "c3",
		PolicyRejected: true,
	})
	raw, _ := peer.ReadFrame()
	var ack ipc.AttachedMsg
	_ = json.Unmarshal(raw, &ack)

	if ack.OK {
		t.Error("attach with PolicyRejected hint must not return OK")
	}
	if ack.Status != ipc.AttachStatusPolicyRejected {
		t.Errorf("Status=%q want %q", ack.Status, ipc.AttachStatusPolicyRejected)
	}
	if ack.NeedsConfirmation {
		t.Error("policy_rejected is terminal, not a proposal")
	}
	// Side-effect check: no channel CreateTopic / ValidateTopic call.
	if len(fc.createCalls) > 0 || len(fc.validateCalls) > 0 {
		t.Errorf("policy_rejected must short-circuit; got create=%d validate=%d",
			len(fc.createCalls), len(fc.validateCalls))
	}
}
```

- [ ] **Step 2: Run test to verify failure**

Run: `go test ./internal/broker/ -run TestAttach_PolicyRejectedHint -count=1 -v`
Expected: FAIL — Status is empty because broker doesn't handle the hint.

- [ ] **Step 3: Add short-circuit branch at the top of handleAttach**

Edit `internal/broker/attach.go`. In `handleAttach`, immediately after the `json.Unmarshal` of `req` (right after the malformed-attach branch), add:

```go
	// Policy-rejected hint: the CLI host's policy layer rejected the
	// prior attach (e.g. Codex approvals_reviewer="auto_review"). The
	// adapter is re-invoking with this hint so we surface a clean
	// structured status — no claim, no validate, no topic registration.
	// The broker can't detect the underlying rejection itself (it lives
	// upstream of the adapter); the hint is the agent's observation
	// passed through. See docs/plans/2026-05-19-codex-policy-3state.md.
	if req.PolicyRejected {
		_ = conn.WriteJSON(ipc.AttachedMsg{
			Op: ipc.OpAttached, OK: false,
			Status: ipc.AttachStatusPolicyRejected,
			Err:    "CLI host policy layer rejected attach; tenant admin must approve the Telegram destination before retry",
		})
		return
	}
```

The branch goes BEFORE the channel resolution (line 45) so it short-circuits unconditionally.

- [ ] **Step 4: Run test to verify pass**

Run: `go test ./internal/broker/ -run TestAttach_PolicyRejectedHint -count=1 -v`
Expected: PASS.

- [ ] **Step 5: Run full broker tests**

Run: `go test ./internal/broker/ -count=1`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add internal/broker/attach.go internal/broker/attach_test.go
git commit -m "broker: short-circuit attach when PolicyRejected hint is set"
```

---

## Task 5: Claude adapter formatter renders new statuses

**Files:**
- Modify: `cmd/c3-claude-adapter/main.go` (function `formatAttached`)
- Test: `cmd/c3-claude-adapter/wire_test.go`

- [ ] **Step 1: Write the failing tests**

Append to `cmd/c3-claude-adapter/wire_test.go`:

```go
func TestFormatAttached_NoTopicsConfigured(t *testing.T) {
	msg := &ipc.AttachedMsg{
		Op:     ipc.OpAttached,
		OK:     false,
		Status: ipc.AttachStatusNoTopicsConfigured,
		Err:    "attach dm: channels.telegram.dm_chat_id not set in mappings.json",
	}
	got := formatAttached(msg)
	wantSubstrings := []string{"not configured", "c3-broker setup"}
	for _, w := range wantSubstrings {
		if !strings.Contains(got, w) {
			t.Errorf("formatAttached(no_topics_configured) missing %q in %q", w, got)
		}
	}
}

func TestFormatAttached_PolicyRejected(t *testing.T) {
	msg := &ipc.AttachedMsg{
		Op:     ipc.OpAttached,
		OK:     false,
		Status: ipc.AttachStatusPolicyRejected,
		Err:    "CLI host policy layer rejected attach; tenant admin must approve",
	}
	got := formatAttached(msg)
	wantSubstrings := []string{"policy", "tenant admin", "approve"}
	for _, w := range wantSubstrings {
		if !strings.Contains(got, w) {
			t.Errorf("formatAttached(policy_rejected) missing %q in %q", w, got)
		}
	}
}
```

Make sure the import block has `"strings"` and `"github.com/karthikeyan5/c3/internal/ipc"`. Check the existing imports first.

- [ ] **Step 2: Run tests to verify failure**

Run: `go test ./cmd/c3-claude-adapter/ -run 'TestFormatAttached_(NoTopicsConfigured|PolicyRejected)' -count=1 -v`
Expected: FAIL — formatAttached returns generic "attach failed:" prefix because it doesn't branch on Status.

- [ ] **Step 3: Update formatAttached to branch on Status before falling through to Err**

Edit `cmd/c3-claude-adapter/main.go`. In `formatAttached` (around line 995), replace the final two branches (the `if a.Err != ""` and `return "attach: unspecified failure"`) with a Status-aware version:

```go
	// Status-aware structured failures. Added 2026-05-19 so the agent can
	// tell the user "you need to run setup" vs "your tenant blocked this".
	switch a.Status {
	case ipc.AttachStatusNoTopicsConfigured:
		return fmt.Sprintf("C3 is not configured for this destination. Run `c3-broker setup` to wire up the Telegram bot token, group chat id, and a starter topic, then retry attach. (broker said: %s)", a.Err)
	case ipc.AttachStatusPolicyRejected:
		return fmt.Sprintf("Attach rejected by your CLI host's policy layer. The Telegram destination needs tenant-admin approval before this CLI can attach. Ask the tenant admin to approve the destination, then retry attach. (host said: %s)", a.Err)
	}
	if a.Err != "" {
		return "attach failed: " + a.Err
	}
	return "attach: unspecified failure"
```

- [ ] **Step 4: Run tests to verify pass**

Run: `go test ./cmd/c3-claude-adapter/ -run 'TestFormatAttached_(NoTopicsConfigured|PolicyRejected)' -count=1 -v`
Expected: PASS.

- [ ] **Step 5: Run full adapter tests**

Run: `go test ./cmd/c3-claude-adapter/ -count=1`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add cmd/c3-claude-adapter/main.go cmd/c3-claude-adapter/wire_test.go
git commit -m "claude-adapter: render no_topics_configured and policy_rejected statuses"
```

---

## Task 6: Codex adapter formatter parity + policy_rejected tool argument

**Files:**
- Modify: `cmd/c3-codex-adapter/main.go` (functions `formatAttached`, `toolAttach`, `buildMCPServer`)
- Test: `cmd/c3-codex-adapter/wire_test.go`

- [ ] **Step 1: Write the failing formatter tests**

Append to `cmd/c3-codex-adapter/wire_test.go`:

```go
import (
	"strings"
)

func TestCodexFormatAttached_NoTopicsConfigured(t *testing.T) {
	msg := &ipc.AttachedMsg{
		Op:     ipc.OpAttached,
		OK:     false,
		Status: ipc.AttachStatusNoTopicsConfigured,
		Err:    "attach dm: channels.telegram.dm_chat_id not set in mappings.json",
	}
	got := formatAttached(msg)
	for _, w := range []string{"not configured", "c3-broker setup"} {
		if !strings.Contains(got, w) {
			t.Errorf("formatAttached(no_topics_configured) missing %q in %q", w, got)
		}
	}
}

func TestCodexFormatAttached_PolicyRejected(t *testing.T) {
	msg := &ipc.AttachedMsg{
		Op:     ipc.OpAttached,
		OK:     false,
		Status: ipc.AttachStatusPolicyRejected,
		Err:    "CLI host policy layer rejected attach; tenant admin must approve",
	}
	got := formatAttached(msg)
	for _, w := range []string{"policy", "tenant admin", "approve"} {
		if !strings.Contains(got, w) {
			t.Errorf("formatAttached(policy_rejected) missing %q in %q", w, got)
		}
	}
}

func TestCodexAttachTool_AcceptsPolicyRejectedArg(t *testing.T) {
	// Wire-shape guard: the agent must be able to pass
	// `policy_rejected=true` to the `attach` tool. If the input schema
	// blocks it, the agent will never be able to surface the host
	// rejection cleanly. InputSchema is registered as map[string]any
	// (same pattern as every other tool in this adapter — see
	// buildMCPServer). We list tools through the SDK and assert the
	// `policy_rejected` property is advertised.
	a := newAdapter()
	srv := a.buildMCPServer()
	if srv == nil {
		t.Fatal("buildMCPServer returned nil")
	}

	clientT, serverT := mcp.NewInMemoryTransports()
	a.transport = newLogNotifyTransport(serverT)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	go func() { _ = srv.Run(ctx, a.transport) }()

	client := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "0.0.0"}, nil)
	sess, err := client.Connect(ctx, clientT, nil)
	if err != nil {
		t.Fatalf("client.Connect: %v", err)
	}
	defer sess.Close()

	listResult, err := sess.ListTools(ctx, nil)
	if err != nil {
		t.Fatalf("ListTools: %v", err)
	}
	var attachTool *mcp.Tool
	for _, tool := range listResult.Tools {
		if tool.Name == "attach" {
			attachTool = tool
			break
		}
	}
	if attachTool == nil {
		t.Fatal("attach tool not advertised")
	}
	// InputSchema is serialized over the wire. Through the SDK ListTools
	// path it surfaces as a *jsonschema.Schema. Marshal to JSON and walk
	// the raw structure so we don't bind to internal type names.
	raw, err := json.Marshal(attachTool.InputSchema)
	if err != nil {
		t.Fatalf("marshal attach InputSchema: %v", err)
	}
	if !strings.Contains(string(raw), `"policy_rejected"`) {
		t.Errorf("attach input schema missing 'policy_rejected' property; schema=%s", raw)
	}
}
```

Imports needed: `"encoding/json"`, `"strings"`, `"context"`, `"time"`, plus the existing `"github.com/modelcontextprotocol/go-sdk/mcp"` and `"github.com/karthikeyan5/c3/internal/ipc"`. (Several may already be in the file — check the existing `wire_test.go` imports before adding.)

- [ ] **Step 2: Run formatter tests to verify failure**

Run: `go test ./cmd/c3-codex-adapter/ -run 'TestCodexFormatAttached_(NoTopicsConfigured|PolicyRejected)' -count=1 -v`
Expected: FAIL — formatter doesn't branch on Status; tool schema lacks the field.

- [ ] **Step 3: Update Codex formatAttached identically to Claude's**

Edit `cmd/c3-codex-adapter/main.go`. In `formatAttached` (around line 726), replace the final two branches with the Status-aware version (same prose as Claude adapter for cross-CLI parity — Karthi's "same flow to work in Codex" principle):

```go
	switch a.Status {
	case ipc.AttachStatusNoTopicsConfigured:
		return fmt.Sprintf("C3 is not configured for this destination. Run `c3-broker setup` to wire up the Telegram bot token, group chat id, and a starter topic, then retry attach. (broker said: %s)", a.Err)
	case ipc.AttachStatusPolicyRejected:
		return fmt.Sprintf("Attach rejected by your CLI host's policy layer. The Telegram destination needs tenant-admin approval before this CLI can attach. Ask the tenant admin to approve the destination, then retry attach. (host said: %s)", a.Err)
	}
	if a.Err != "" {
		return "attach failed: " + a.Err
	}
	return "attach: unspecified failure"
```

- [ ] **Step 4: Add the `policy_rejected` argument to the attach tool registration**

In `cmd/c3-codex-adapter/main.go`, find the `attach` tool registration inside `buildMCPServer` (around line 514). The registration uses `InputSchema: map[string]any` (verified — same pattern as every other tool in the adapter). Append the new property and refresh the description. Edit the block:

```go
		{
			tool: &mcp.Tool{
				Name:        "attach",
				Description: "Attach this Codex session to a Telegram topic. Same proposal-flow semantics as Claude Code's attach.",
				InputSchema: map[string]any{
					"type": "object",
					"properties": map[string]any{
						"target":   map[string]any{"type": "string"},
						"name":     map[string]any{"type": "string"},
						"topic_id": map[string]any{"type": "integer"},
						"group":    map[string]any{"type": "string"},
						"create":   map[string]any{"type": "boolean"},
						"steal":    map[string]any{"type": "boolean"},
					},
				},
			},
			handler: a.toolAttach,
		},
```

to:

```go
		{
			tool: &mcp.Tool{
				Name:        "attach",
				Description: "Attach this Codex session to a Telegram topic. Same proposal-flow semantics as Claude Code's attach. If your CLI host's policy layer rejects this call (e.g. Codex approvals_reviewer=auto_review surfacing 'unacceptable risk rejection'), re-invoke with `policy_rejected=true` so the user sees the actionable next-step (tenant admin approval) rather than a silent failure.",
				InputSchema: map[string]any{
					"type": "object",
					"properties": map[string]any{
						"target":          map[string]any{"type": "string"},
						"name":            map[string]any{"type": "string"},
						"topic_id":        map[string]any{"type": "integer"},
						"group":           map[string]any{"type": "string"},
						"create":          map[string]any{"type": "boolean"},
						"steal":           map[string]any{"type": "boolean"},
						"policy_rejected": map[string]any{"type": "boolean"},
					},
				},
			},
			handler: a.toolAttach,
		},
```

Inside `toolAttach` (around line 656), append the argument decode right after the existing `steal` block:

```go
	if v, ok := args["policy_rejected"].(bool); ok {
		attachReq.PolicyRejected = v
	}
```

- [ ] **Step 5: Run all tests in Codex adapter**

Run: `go test ./cmd/c3-codex-adapter/ -count=1 -v`
Expected: PASS for all formatter tests; tool schema test PASS iff the schema is correctly extended.

- [ ] **Step 6: Commit**

```bash
git add cmd/c3-codex-adapter/main.go cmd/c3-codex-adapter/wire_test.go
git commit -m "codex-adapter: render policy_rejected/no_topics_configured + accept policy_rejected hint"
```

---

## Task 7: DEBUGGING.md — operator-facing docs

**Files:**
- Modify: `DEBUGGING.md` (at repo root, not under docs/)

- [ ] **Step 1: Identify insertion point**

Run: `grep -n '^## ' DEBUGGING.md`

Expected sections include: TL;DR, Logs, Log line shape, Content policy, Common diagnostic flows, Persistent state files, STT handler path resolution.

Insert the new section AFTER "Common diagnostic flows" and BEFORE "Persistent state files".

- [ ] **Step 2: Append the new section**

Add this content as a new section. Find the line "## Persistent state files" and insert above it:

````markdown
## Codex policy layer rejected attach

**Failure mode (Sakthi's install pilot, 2026-05-16):** Codex was configured
with `approvals_reviewer = "auto_review"` (or `"guardian_subagent"`) in
`~/.codex/config.toml`. A fresh `attach` tool call from a Codex session was
silently classified as an "unacceptable risk" (data-export class — bot
tokens, chat ids in the response) and rejected by Codex's policy layer
before reaching `c3-codex-adapter`. The agent saw the rejection in the
Codex UI but didn't relay an actionable next-step to the user. Manual
retry only succeeded after the tenant admin approved the Telegram
destination tenant-side.

Note: `C3_CODEX_REMOTE_BRIDGE` and `C3_CODEX_ALLOW_MANUAL_FORWARD` are
NOT involved. Those env vars gate the WebSocket forwarder to the
codex-app-server; the policy layer sits well upstream of any C3 wire.

**How c3 surfaces it now (2026-05-19):** the `attach` MCP tool accepts an
explicit `policy_rejected=true` argument. The agent observing the host's
rejection re-invokes `attach(policy_rejected=true, ...)`; the broker
short-circuits with `AttachedMsg.Status = "policy_rejected"` and the
adapter formats:

> "Attach rejected by your CLI host's policy layer. The Telegram
> destination needs tenant-admin approval before this CLI can attach.
> Ask the tenant admin to approve the destination, then retry attach."

This replaces the prior silent-fail / generic-error mode where the user
couldn't distinguish "broker isn't configured" from "tenant policy blocked
the call" from "broker succeeded but delivery dropped."

**Why the adapter can't detect it itself.** Investigated 2026-05-19; see
`docs/plans/2026-05-19-codex-policy-3state.md` Phase 0. Codex's policy
state lives in `~/.codex/config.toml` (host-owned, not exposed via MCP)
and any per-request decision happens upstream of the spawned MCP
server. When Codex rejects, our adapter never even receives the tool
call. Only the agent (LLM) sees the rejection in its turn output, so
the agent is the right vector to surface the structured hint.

**How the user resolves:**

1. Tenant admin reviews the request and approves the Telegram
   destination (chat id + bot token) for the specific Codex tenant /
   project.
2. After approval, retry `attach` (the agent re-invokes without the
   `policy_rejected` hint).
3. If retry still rejects: confirm `approvals_reviewer` in
   `~/.codex/config.toml` and the per-tool `approval_mode` for
   `mcp_servers.c3_codex.tools.attach` is appropriate for the
   approved destination.

**Distinguishing from "no topics configured":** that's a separate state
(`AttachedMsg.Status = "no_topics_configured"`) — the broker has zero
channels or destinations registered yet. Fix is `c3-broker setup`, not
tenant approval. The adapter formatter renders both cases with
distinguishable user-facing prose.
````

- [ ] **Step 3: Verify the section renders sanely**

Run: `grep -n '^## ' DEBUGGING.md`
Expected: the new section appears between "Common diagnostic flows" and "Persistent state files".

- [ ] **Step 4: Commit**

```bash
git add DEBUGGING.md
git commit -m "DEBUGGING: document Codex policy-layer rejected-attach diagnosis"
```

---

## Task 8: Full verification suite

**Files:** none (just running tests)

- [ ] **Step 1: Run race-enabled full test suite**

Run: `go test -count=1 -race ./...`
Expected: PASS for all packages.

- [ ] **Step 2: Run go vet**

Run: `go vet ./...`
Expected: zero output (clean).

- [ ] **Step 3: Run go build**

Run: `go build ./...`
Expected: zero output (clean).

- [ ] **Step 4: (Optional) Re-build binaries to make sure cmd/c3-broker, cmd/c3-claude-adapter, cmd/c3-codex-adapter compile to working artifacts**

Run: `go install ./cmd/...`
Expected: zero output.

If any of these fail, stop and report. Don't paper over a failure.

- [ ] **Step 5: Self-review the diff via git diff**

Run: `git log --oneline | head -10 && git diff main...HEAD --stat`
Read the diff with fresh eyes. Check:

- All AttachedMsg success paths set `Status: ipc.AttachStatusOK`.
- The no-config branch in `handleAttach` and `attachDM` both set `Status: ipc.AttachStatusNoTopicsConfigured`.
- The `PolicyRejected` short-circuit fires before any channel resolution.
- Both adapter formatters branch on Status before Err.
- `DEBUGGING.md` is at the repo root, not under `docs/`.
- No spurious changes to proposal-flow shapes (`create`, `disambiguate_dm`, `force_steal`, `use_existing_other_group`).

---

## Out of scope

- No changes to mode protocol / ping (TODO item R).
- No changes to shim install (TODO item Q).
- No onboarding-flow changes (TODO item T).
- No attempt to "fix" Codex's policy layer.
- No commit aggregation / squash / push. The plan ends with N small commits on the working branch.

## Time budget

2-3 hours. If past 3h, stop and report progress in the report message.
