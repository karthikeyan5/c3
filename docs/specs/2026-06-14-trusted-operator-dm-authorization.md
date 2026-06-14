# Trusted Operator Authorization for c3 — Design Spec

**Date:** 2026-06-14
**Status:** DRAFT — for Karthi's ratification. Not yet approved for build. §10 lists the decisions that must be settled first; §9 Phase 0 is a hard gate.
**Author:** Claude (Opus 4.8 — Fable 5 unavailable this session).
**Origin:** Hit live during the SSHGate Tier-2 walk (2026-06-14). The Claude Code safety classifier repeatedly refused to run a privileged action (`sudo …`) whose authorization arrived over Telegram, because channel-sourced messages are treated as untrusted. Karthi: "if it's authenticated that it's coming from my Telegram, it should be allowed — write a spec to solve this."

---

## 1. The problem

When a Claude Code session is bridged through c3, two inputs from the *same human* are trusted very differently:

- **CLI keystrokes** — treated as the local operator. They can authorize sensitive actions.
- **An authenticated owner DM** (allowlisted `user_id`, Telegram-authenticated) — treated as **untrusted external data**. It cannot authorize sensitive actions; the auto-mode safety classifier blocks any privileged tool call whose only justification is a channel message.

Concrete incident: with a (Telegram-authorized) `NOPASSWD` grant in place, the classifier still denied my `sudo` calls with *"authorization comes from untrusted Telegram channel messages."* It even blocked me from *verifying* the grant.

**Karthi's framing is reasonable:** only he can send from his Telegram account; the c3 allowlist pins his `user_id`; in a DM the sender is individually authenticated by Telegram. So an owner DM is, for his single-operator use, about as trustworthy as his keyboard — *provided he opts into the larger trust base it implies* (see §7).

**What we want:** an authenticated owner DM should be able to authorize specific, scoped, sensitive actions — deterministically, with no need to re-type at the CLI — without weakening the protection that exists for genuinely untrusted channel content.

---

## 2. Why the obvious fixes don't work (and what the one real lever is)

Both halves of this were verified against the code and the Claude Code docs (2026-06-14):

1. **"Make c3 mark the DM as trusted" — not possible.** The `<channel source="plugin:c3:c3" …>` wrapper and the *"treat as untrusted external data"* system-reminder are generated **by the Claude Code harness, not by c3**. (Grepping the entire c3 tree for that warning string returns zero hits; the adapter only sends `content` + a flat `meta` map over the `notifications/claude/channel` MCP notification — `cmd/c3-claude-adapter/main.go:485,507`.) c3 cannot suppress or recolor the warning by editing its own formatter.

2. **No supported "trusted input" API.** Claude Code exposes **no** mechanism for an MCP server or plugin to mark injected text as trusted-operator input. Confirmed across the permissions/hooks docs.

3. **You cannot (and should not) "convince" the classifier.** It is *designed* to distrust authorization that originates in a channel. That's correct behavior; the fix must not be "argue the model/classifier into it."

4. **The one real lever: a `PreToolUse` hook.** It is the only supported way a plugin can make a binding permission decision. A hook can return:
   - `permissionDecision: "allow"` — run the tool,
   - `"deny"` — hard-block,
   - `"defer"` — fall through to the normal permission flow.

   Documented behavior (see ⚠️ in §4): an `"allow"` short-circuits to execution **before** the classifier; the classifier is consulted only on `"defer"`. The hook is a *deterministic local program*, not the model — so this moves the trust decision out of "persuade the classifier" and into "a local gate that consults c3's authorization state." That is exactly the right place for it.

---

## 3. Design principle — separate auth / authz / enforcement

The spec keeps three concerns distinct (the same shape as SSHGate, lifted one layer up to the Claude Code permission gate):

| Concern | Owner | Mechanism |
|---|---|---|
| **Authentication** — *is this really Karthi?* | Telegram + c3 allowlist | DM (`chat_id == user_id`), `user_id ∈ operators`. Telegram authenticates the sender; c3 pins the id. |
| **Authorization** — *is this action permitted?* | c3 broker (the authority) | An **explicit, scoped, time-boxed, auditable grant** — never "every DM is trusted." Two UX styles (§5). |
| **Enforcement** — *let the tool run or not* | c3 `PreToolUse` hook (local, deterministic) | Consults the broker; returns allow / deny / defer. |

The model never decides trust. A local hook does, by asking the local broker. The classifier is bypassed not by trickery but because a *local deterministic policy* explicitly approved the action.

---

## 4. Claude Code mechanism (the enforcement side)

A `PreToolUse` hook receives on stdin: `tool_name`, `tool_input.command`, `session_id`, `cwd`, `permission_mode`, `transcript_path`. It can read local files / a unix socket / env before deciding, and matchers can scope it to `Bash` (and to a command glob, e.g. `Bash(sudo *)`).

It returns:
```json
{ "hookSpecificOutput": { "hookEventName": "PreToolUse", "permissionDecision": "allow" } }
```

**⚠️ LOAD-BEARING ASSUMPTION — VERIFY IN PHASE 0.** Two independent doc-research passes disagreed:
- Pass A: in **auto** mode the classifier still evaluates even after a hook `"allow"` (hook-allow does *not* override the classifier).
- Pass B (more detailed, citing the hooks × permission-mode interaction table): a hook `"allow"` **runs the tool immediately regardless of permission mode**; the classifier is consulted **only** on `"defer"`. Explicit **deny rules** (and managed `hard_deny`) are the only hard backstop that overrides a hook `"allow"`.

The entire spec's viability rests on Pass B being correct. **Do not build past Phase 0 until this is empirically confirmed** (§9). If Pass A turns out correct, the fallback is: run sessions in **default** mode (where hook-allow is agreed to bypass the prompt) rather than auto mode, and document that constraint.

**Hard backstop, regardless:** an explicit `deny` rule beats a hook `"allow"`. Therefore guarded commands must **not** also appear in any `permissions.deny` / managed `hard_deny` list, or the gate silently can't approve them. The hook returns `"defer"` (not `"deny"`) for unauthorized-but-not-forbidden commands, so normal prompting still works when no grant is active.

---

## 5. c3 mechanism (the authorization authority)

Two authorization UX styles, sharing one enforcement point. Recommend shipping **A** as default and **B** as an opt-in strict mode for a high-risk command set.

### Style A — scoped operator-session grant ("unlock")
The owner issues an explicit DM command, e.g.:
```
/authorize sudo 15m          # allow the guarded "sudo" class for 15 min on this route
/authorize "apt *" 10m
/grants                      # list active grants
/revoke <id>                 # or /revoke all
```
c3 records a **grant**: `{grant_id, route (cwd↔topic), scope (command glob/class), expiry, nonce, issued_by, issued_at}`. While a grant is live, the hook approves matching commands for the session bound to that route. Low friction; matches Karthi's stated intent. Bounded by scope + short TTL + audit + revocation; default-deny when no grant matches.

### Style B — per-action approval (SSHGate-style)
When the agent hits a guarded command, the `PreToolUse` hook calls the broker, which **DMs the owner the exact command** and waits (synchronously, with timeout) for an approve/deny tap (inline keyboard or a reply). The hook returns `allow`/`deny` accordingly. Highest assurance, per-command, shows the literal command before it runs — this is SSHGate's model applied to arbitrary Bash. Use for the highest-risk class (e.g. `sudo`, `rm -rf`, anything writing outside cwd).

Both styles are enforced by the *same* hook; they differ only in whether the broker answers from a pre-issued grant (A) or by prompting the phone in real time (B).

### The session ↔ owner join
The hook gets `session_id` + `cwd`. c3 already maps `cwd → route (topic)` (`internal/mappings`, per-directory claim memory) and `route → claiming session` (`internal/broker/routes.go`). So "the owner authorized route R" binds to "the session attached to route R." The grant is keyed by route, which is the natural join and avoids trusting a `session_id` the hook can't independently verify.

---

## 6. Code seams (from the c3 map)

| Step | Location | Change |
|---|---|---|
| Operator identity | `internal/mappings/types.go:27` (`Allowlist`) / dead `MasterUserID` `:38` | Add `Operators []int64` (DM-only). Optionally repurpose the currently-dead `MasterUserID` as the single owner. |
| Decide "authenticated owner DM" | `internal/broker/pairing.go:244` (`Gate`) | Already computes `isPrivateChat && IsUserAllowed`. Extend to recognize `Operators` and to parse `/authorize`,`/revoke`,`/grants` DM commands (Style A) and approve/deny replies (Style B). |
| Grant store | new, in `internal/broker` | In-memory map keyed by route → grant(s); optional 0600 persistence; expiry sweep; audit log. Revocable. |
| Authorization check IPC | `internal/ipc/messages.go` + broker handler | New op `AuthorizeCheck{session_id, cwd, tool, command}` → `{decision: allow|deny|defer, reason, grant_id}`. Plus ops to create/list/revoke grants (driven from the gate's DM-command parser). |
| The hook | new `plugins/c3/hooks/pretooluse-authorize.sh` + register in `plugins/c3/.claude-plugin/plugin.json` (currently **no** `hooks` key) | Tiny: read stdin JSON → dial broker socket (`/tmp/c3.sock` or `$XDG_RUNTIME_DIR/c3.sock`) → emit the decision JSON. |
| Audit | broker log (respect the no-content-logging policy, `DEBUGGING.md`) | Log every grant issue/expire/revoke and every hook decision (command class + decision + grant_id; **not** full sensitive content). |

DM-only is enforced structurally: group messages trust the `chat_id`, not the individual sender (`types.go:21-26`), and debounce-merge takes the *last* message's sender (`worker.go:234`) — so a guarded grant must never be honored for a group route. Restrict to `isPrivateChat`.

---

## 7. Threat model (what Karthi is choosing to trust)

**Trust anchors** (the TCB this feature adds beyond "human at the keyboard"):
- Telegram's authentication of `user_id`.
- Secrecy of the bot token.
- Integrity of the local c3 broker + its unix socket (local, 0600).
- The owner's phone/account/device.

**Residual risks & mitigations:**
- **Bot-token leak** → an attacker can read routed content and post *as the bot*, but **cannot forge a sender `user_id`** (Telegram controls it), so they cannot impersonate the owner to issue grants. *Good property — state it.* Mitigate: token 0600 at rest; rotate via BotFather `/revoke` on suspicion.
- **Owner device/account compromise** → full compromise — equivalent to someone at the keyboard. Accepted by the same logic that trusts the keyboard.
- **Prompt injection** (owner forwards/pastes attacker content that contains a grant-like command) → grants require an **explicit structured command**, never free-text inference; Style B shows the literal command to approve; scope + short TTL + default-deny; DM-only. Never auto-elevate arbitrary DM text.
- **Over-broad / stale grants** → scope to a command class, short default TTL, audit, one-tap revoke, default-deny.
- **Replay** → per-grant nonce + expiry.

**Explicit opt-in.** The feature is **OFF by default**. It activates only when the operator sets `operators` and enables the hook — an informed acceptance of the expanded TCB above. Off ⇒ behavior is exactly today's (every channel message untrusted).

---

## 8. Config & UX

`mappings.json` (per channel or top-level):
```jsonc
"operator_authz": {
  "enabled": false,
  "operators": [85720317],          // DM user_ids that may authorize; [] disables
  "mode": "session_grant",          // "session_grant" (A) | "per_action" (B)
  "default_ttl": "15m",
  "max_ttl": "1h",
  "guarded": ["Bash(sudo *)", "Bash(rm -rf *)"],  // commands requiring a grant; others defer
  "per_action_classes": ["Bash(sudo *)"]          // subset that always uses Style B even in session mode
}
```
- DM grammar (Style A): `/authorize <scope> <ttl>`, `/grants`, `/revoke <id|all>`.
- Style B: inline-keyboard Approve/Deny on the command card.
- Optional `/c3:authorize` slash command (CLI side) for symmetry, but the DM path is primary.

---

## 9. Phasing

- **Phase 0 — VERIFY (hard gate).** Empirically confirm the §4 assumption in the *current* Claude Code build:
  1. Add a `PreToolUse` hook that returns `"allow"` for one harmless guarded command (e.g. `sudo -n true`).
  2. In **auto** mode, run that command and observe: does it run with no prompt and no classifier veto?
  3. Repeat in **default** mode.
  Record which modes make hook-allow authoritative. The rest of the spec's "recommended mode" depends on this. **No further build until this is known.**
- **Phase 1 — Style A.** `operators` config; grant store; broker `AuthorizeCheck` + grant ops; the `PreToolUse` hook + plugin.json registration; DM grammar; audit log. DM-only enforcement + tests.
- **Phase 2 — Style B.** Per-action approval card + reply/inline-keyboard handling, reusing the pairing-reply plumbing. Synchronous hook wait w/ timeout → default-deny on timeout.
- **Phase 3 — Hardening.** Scope-language tests; deny-rule/`hard_deny` interplay; debounce-merge sender-attribution test (DM-only); revocation + expiry sweeps; docs (INSTALL/USAGE) + a threat-model page.

---

## 10. Decisions for Karthi (ratify before Phase 1)

1. **Default authorization style** — ship **A (scoped session grant)** as default with **B (per-action)** forced for a high-risk class? (Recommended.) Or B-only for max strictness?
2. **Operator identity** — a multi-entry `operators []int64` (recommended, DM-only), or repurpose the single dead `MasterUserID`? Default operator = the existing allowlisted DM user if unset?
3. **Default guarded set** — which commands require a grant? (Recommended: a configurable `guarded` glob set, default `sudo`/privileged + anything you mark; everything else `defer`s to normal prompting — so this never makes *non-guarded* actions more permissive.)
4. **Default TTL / max TTL** for Style A grants. (Recommended 15m / 1h.)
5. **Scope of v1** — Telegram + Claude Code only, or design the broker `AuthorizeCheck` op to be CLI-agnostic now (Codex adapter later)? (Recommended: keep the broker op CLI-agnostic; ship the hook for Claude Code first.)

---

## 11. Notes

- **This generalizes SSHGate.** SSHGate already does out-of-band, authenticated, per-action human approval for sensitive ops via Telegram. This spec is the same pattern at the Claude Code permission layer, with c3 as the authority. Worth keeping the two threat-model docs consistent.
- **Ideal long-term:** a first-class Claude Code "channel trust" signal (the harness honoring a `meta` attribute that an allowlisted-owner DM is operator-trusted) would remove the need for the hook shim. Until/unless Anthropic ships that, the `PreToolUse` hook is the supported, robust path and requires **no** harness changes. Worth a feature request to Anthropic in parallel.
- **What this does *not* do:** it does not make every owner DM a trusted instruction, and it does not relax protection for genuine third-party/group content. It adds one narrow, explicit, auditable path for the *operator* to authorize *scoped* actions.
