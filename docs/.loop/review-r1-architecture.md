# r1 — Architecture Coherence Review

Spec under review: `docs/specs/2026-05-08-c3-rearch-design.md` v5.

## Verdict

**Yes with these fixes.** The spec is structurally coherent at the planes-and-contracts level: five planes, one mapping file, one IPC protocol, one Channel interface, one plugin host. End-to-end traces hang together. The fixes needed are mostly type/name drift between §4.1, §4.4, §4.5, and §6 plus a handful of unstated mechanisms (claim ownership, broker-spawning, debounce-vs-route ordering). None of these are structural — none rip up a plane — but several would bite during implementation if left to "obvious during coding." Worth one focused revision pass before Phase 3 starts.

## Contradictions

- **General topic id, 1 vs nil.** §4.2 says "`message_thread_id` is `*int64` … `1` is the General forum topic, `>1` is custom topic," but §6 says "General topic id is **1**, not 0 — fixed end-to-end" without saying whether General is encoded as `nil` or `int64(1)` in the route key. §5.6 stores `topic_id: 281` and §5.5 claims `(telegram, dm_chat_id, nil)` for DM, leaving General ambiguous. Pick one — `*int64(1)` for General, `nil` only for non-forum/DM — and say so once.
- **Codex bridge language banner vs. the Codex section.** The header (line 4) says "no part of the POC carries forward" and §11.D says the Codex bridge is fully Go. The architecture diagram in §4 still labels the Codex column "(Python POC) shim → supervisor → stub.py" and the prose right under it says "the implementation language is just Python because the POC works." That contradicts §4.4's `cmd/codex/main.go` + `cmd/c3-codex-adapter/main.go` Go binaries.
- **Plugin priority ordering.** §8 hook table says `OnInbound` is "chained; first non-drop result wins" and §8 final paragraph says "Plugin order is config-driven (`mappings.json:plugins.<name>.priority`)." `mappings.json` schema in §4.3 has no `priority` field on the plugin entries.
- **Auto-attach response shape.** §4.4 IPC table says `hello_ack` carries `{auto_attached, mapping?, claim_holder?, no_config?}`. §5.2 step 2 has the broker reply `{auto_attached: false, no_mapping: true}` — `no_mapping` isn't in the documented `hello_ack` fields.

## Gaps

- **Claim lifecycle is undefined.** §4.2 says "live routes are in-memory only (`map[RouteKey]*Stub`)" and §5.6 talks about `claim_holder`, but no section defines: when does a claim get released (stub disconnect? broker restart? explicit detach?), what happens to in-flight inbound when a claim is released, and is there a single-claim-per-route invariant or can multiple stubs subscribe. Belongs in §4.2 or a new §4.2.1.
- **Broker spawn / singleton mechanism.** §5.1 step 4 says the adapter "spawns `c3-broker` (also via `$PATH`)" and §4.4 mentions "same flock pattern the Claude adapter uses." The flock path, where the broker daemonizes, and how races between two adapters spawning at once are resolved are nowhere defined. Belongs in §4 or §12 phase 3.
- **`OnVoiceReceived` payload type.** §4.5 references `VoicePayload` but the struct isn't defined anywhere in the spec. §6 says "voice handler hands off to STT plugin and uses the transcript as the message text" without specifying whether the channel pre-downloads the audio or hands the plugin a `file_id` to fetch. Belongs in §4.1 or §8.
- **Debounce vs. routing order.** §5.7 lists the steps as `OnInbound → debounce → route lookup → forward`. §7.3 says the debounce window concatenates messages "with the latest `message_id` as canonical." But §4.5 plugin hooks fire `OnInbound` per individual event — does the debounce-merged message re-enter `OnInbound`, or is `OnInbound` only seen by raw events? This determines whether plugins see merged or unmerged input.
- **`edit_progress` placeholder lifecycle.** §6 says broker tracks the placeholder per `(chat_id, topic_id, session)`; §7.2 says per `session`. "Session" isn't defined — is it the stub claim, the agent turn, or the broker process? When does the placeholder get cleared so the next agent turn doesn't edit a stale message?
- **Codex thread discovery race.** §4.4 step 4 of the WebSocket forwarder picks "the most recent" thread when multiple are loaded. If a user runs `codex resume <other>` while a `codex` (current cwd) bridge is forwarding, "most recent" by what clock? Belongs in §4.4 or §5.x.
- **Confirmation expiry.** §5.2 has the broker emit a proposal and wait for `attach(create=true)`. If the user never confirms, what holds the proposal — is it stateless (broker re-derives on the next call) or stateful (broker remembers `widget-foo` was proposed)? §3 implies stateless; §5.2 reads stateful.

## Drift

- **Tool name casing / namespacing.** §4.1 lists outbound tools `reply, react, edit_message, download_attachment, send_typing, edit_progress`. §6 lists `validate_topic, create_topic` as new first-class tools. §4.4 IPC has `tool_call` as the universal forwarder. Codex adapter exposes `c3_attach, c3_topics, c3_inbox, c3_reply, c3_codex_forward` — the `c3_` prefix lives only on the Codex side. Either standardise on the `c3_` prefix everywhere (Claude adapter `attach`/`topics` would become `c3_attach`/`c3_topics`) or call out explicitly that Claude side stays unprefixed.
- **`InboundEvent` vs. plugin signatures.** §4.1 defines `InboundEvent` with `Sender Sender`. §4.5 hook signature is `OnInbound(msg) → msg | drop` — `msg` type isn't bound to `InboundEvent` in the prose. Same for `OnOutbound` — there's no outbound struct defined.
- **Env-var contract for the Codex bridge.** §4.4 lists `C3_CODEX_APP_SERVER_WS, C3_CODEX_CWD, C3_CODEX_REMOTE_BRIDGE, C3_ATTACH_NAME` injected via `-c mcp_servers.c3_codex.env.*`, plus `C3_CODEX_REAL` and `C3_CODEX_ALLOW_MANUAL_FORWARD` mentioned only in prose. No single table lists them with required/optional + default. Recommend a small env-var table in §4.4.
- **`mappings.json` plugin schema vs. `topics`/`mappings` casing.** Schema mixes `chat_id` (snake_case) with top-level keys `channels`/`mappings`/`plugins`. `master_user_id` lives under `channels.telegram.*` but is conceptually channel-agnostic identity. Minor.

## Data flow trace

**Inbound (Telegram voice → Claude turn).** Telegram channel goroutine receives `getUpdates` payload → builds `InboundEvent{Channel:"telegram", ChatID:-100…, TopicID:&281, Attachments:[voice]}`. Channel calls plugin host `OnVoiceReceived` first (per §4.5 — though §5.7 step 1 says `OnInbound` runs first; **race / order question**: if `OnInbound` runs before voice transcription, plugins see empty `Text` and a voice attachment; if after, they see transcribed text). Broker debounces 1.5s, looks up `ROUTES[(telegram,-100…,281)]`, sends `notifications/claude/channel` to the Claude stub. Stub forwards to Claude over MCP. **Concern:** the typing-indicator goroutine (§7.1) keys off "stub busy" — if a second inbound arrives mid-turn, the debounce window vs. typing-indicator vs. claim invariant aren't sequenced anywhere.

**Outbound (Claude `reply` → Telegram).** Claude calls `reply(text=...)` → MCP tool dispatch in adapter → `tool_call` op over `/tmp/c3.sock` → broker resolves stub's claim to `(telegram,-100…,281)` → runs `OnOutbound` chain → calls Telegram channel `SendReply`. **Concern:** if the claim was released between the tool call and the broker processing it (stub crash, broker restart with persisted mappings but in-memory routes empty), §4.4 says the Codex `c3_reply` recovers from broker claim — does the Claude adapter do the same? Spec doesn't say. Also: `edit_progress` placeholder tracking on broker side means a broker restart loses the placeholder mid-turn; the spec doesn't define recovery.

No deadlocks identified. The race surface is concentrated in (a) plugin-vs-debounce ordering and (b) claim-lifetime gaps.

## Recommendation

**Iterate, then proceed.** One focused revision pass to (1) lock the General-topic encoding, (2) scrub the §4 diagram and prose for residual Python-bridge language, (3) add a claim-lifecycle paragraph in §4.2, (4) add `priority` to the plugins schema, (5) define `VoicePayload` and the `OnInbound`-vs-voice ordering, (6) add an env-var table to §4.4. None of these block Phases 1-2 of the foundation plan (skeleton + mappings registry), so Karthi can start the foundation work while the revision happens. Block Phase 3 (broker core + IPC) on the revision being merged, since the IPC/claim/route fixes land there.
