# Review R2 — Architecture Coherence

**Spec:** `/home/karthi/arogara/c3/docs/specs/2026-05-08-c3-rearch-design.md` (v5)
**Scope:** Internal coherence only. Independent of R1.

## Verdict

**Yes, with these fixes.** The five-plane design hangs together, the IPC schema is concrete, and §5's flows trace cleanly. No structural problem forces a redesign — but there is contract drift between §4 and §5/§7, several types referenced in the schema that are never declared, and one real race in the placeholder/typing/STT lifecycle that will bite if not pinned.

## Contradictions

- **§4.4.5 vs §4.5.1 — STT prefix ownership.** §4.4.5 step 6 says inbound text arrives prefixed `[Transcribed voice]: ...`. §4.5.1 says the broker substitutes the transcript into `Inbound.Text` without specifying a prefix. §6 says file_id is "retained in attachment meta". No single source of truth.
- **§4.4.2 vs §4.4.5 — Codex tool list.** §4.4.2 lists ten harmonized tools; §4.4.5 enumerates only five. One list is wrong.
- **§5.7 vs §4.1 — rename violated in-spec.** §4.1 says drop `InboundEvent`; §5.7 then uses `InboundEvent`.
- **§10 vs §4 — "plumbed-but-inert" rename tracking.** §10 says `forum_topic_edited` is "plumbed-but-inert"; neither §4.1 `Channel` nor §4.4.1 IPC defines a rename op.
- **§4.4.4 PIPE_BUF justification.** PIPE_BUF (4096) atomicity is invoked, but `notifications/claude/channel` carrying §7.3 debounced batches (≤50 messages) routinely exceeds 4KB; the writer mutex is the actual safety net.

## Gaps

- **Undeclared types in the schema.** `Stub` (§4.2.1, §4.5.1), `RouteKey` (§4.2.1, §4.4.3, §6), `StateDir` (§4.5.1), `ToolRegistry` methods beyond `Add`. `RouteKey` semantics matter: two `*int64` pointing to 1 must hash and compare equal as map keys for "General topic"; not stated.
- **Proposal-to-confirm handoff is stateless but unkeyed.** §4.5.1 says proposals are recomputed; `AttachReq` carries `Create bool` but no proposal id, no echoed `name`/`group`. If sibling state flips mid-confirm (which §4.5.1 anticipates), the agent can create a topic with a name it never proposed.
- **`reload-config` (§4.5.2) vs edited mappings.** Behavior on next inbound when a held mapping was removed/edited in the file is undefined.
- **`inbox` buffer policy (§4.4.5)** — no cap, no eviction, no ack-timeout. Long-disconnected Codex leaks unbounded.
- **Plugin priority + mutation.** §4.5.1's pointer signature implies threading mutations through the chain; §4.5's table reads as value semantics. Not stated.
- **No per-route serial executor specified.** §7.1 typing, §7.2 placeholder map, §4.5 hook chain, §7.3 debounce flush, and STT subprocess all touch the same route concurrently with no defined serialization (see flow).

## Drift

- **`fallback_cooldown_s` (§4.4.3)**, **`codex.shared_root` (§4.4.5)**, **debounce buffer cap of 50 (§6)** all referenced as configurable/load-bearing but absent from the §4.3 schema example.
- **`ReactArgs` / `EditArgs` (§4.1)** are top-level structs but no `OpReact`/`OpEdit` in §4.4.1's const block — they land as `OpToolCall` payloads. Inconsistent vs `ReplyArgs = Outbound` aliasing.
- **`EditArgs` lacks `TopicID`** while §4.4.1's outbound principle is "always carry `*TopicID`". Consistent with Telegram's API, inconsistent with the spec's rule.
- **`C3_ATTACH_NAME` vs cwd-keyed mapping.** Launcher (§4.4.5) infers a topic *name* from cwd; broker (§5.6) keys mappings by cwd. If user pre-renamed the topic, the contract doesn't say which wins.

## Data flow trace

**Inbound (Telegram voice → Claude).** `getUpdates` → channel emits `Inbound{Attachments:[{Kind:"voice"}], Text:""}` → `OnVoiceReceived` chain (priority, first non-empty wins) → STT plugin shells to Python, returns transcript → broker rewrites `Inbound.Text` → `OnInbound` chain → debounce window (≤50 msgs or 1.5s idle) → `ROUTES` lookup → `notifications/claude/channel` framed under writer-mutex. **Race:** if STT takes >1.5s and a second voice arrives, the second's debounce window can close and flush before the first's transcript returns. Spec doesn't pin whether `OnVoiceReceived` blocks the per-route pipeline or runs concurrently. Concurrent → ordering inversion. Serial → slow STT stalls the route. Either is acceptable; neither is specified.

**Outbound (`edit_progress` mid-turn).** Adapter sends `OpToolCall{Name:"edit_progress"}` → broker checks `map[RouteKey]ProgressPlaceholder` → on first call, `channel.SendReply` and store `MessageID`; on subsequent, `channel.EditMessage`. §7.1's typing ticker fires `send_typing` on the same route every 4s, sharing no lock with `edit_progress`. **Real race:** §6 decrements the typing counter on adapter disconnect; §4.5.1 clears the placeholder entry on release. No ordering between counter decrement, placeholder clear, and an in-flight `EditMessage` whose 200 arrives *after* the route is re-claimed by another stub — a late response can repopulate the placeholder map for a route now owned by a different session. Fix: one per-route serial executor owning inbound pipeline, outbound channel calls, placeholder mutation, typing-ticker state.

## Recommendation

**Iterate** — one-pass cleanup, not redesign. Before phase 4 starts:

1. Kill `InboundEvent`; declare `RouteKey`, `Stub`, `StateDir`, `ToolRegistry` in §4.4.1/§4.5.1; specify `*int64` map-key equality.
2. Define proposal echo-back (or proposal token) for `attach(create=true)`.
3. Mandate one per-route serial executor — covers typing/edit_progress/STT/debounce ordering in one stroke.
4. Add `fallback_cooldown_s`, `codex.shared_root`, debounce buffer-cap to §4.3 schema; nest CLI-side keys under a top-level section.
5. Reconcile §4.4.2 vs §4.4.5 Codex tool lists; specify whether STT writes the `[Transcribed voice]:` prefix.

None block phases 1-3; all should land before the routing pipeline is wired in phase 4.
