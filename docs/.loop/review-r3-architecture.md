# R3 Architecture Coherence Review — `2026-05-08-c3-rearch-design.md`

Independent fresh-context review. No prior rounds consulted.

## Verdict

**Ready with these fixes.** The spec is internally coherent at the macro level — five planes are clearly bounded, the Channel/IPC/Plugin contracts are concrete enough to compile against, and the trickiest concurrency hazards are addressed by the §4.2.0 per-route serial executor. A handful of small contract drifts and one architectural gap should be tightened before code starts; none require restructuring.

## Lock-blocking issues

1. **`Stub.ConnID` defined but lifecycle unspecified.** §4.5.1 declares `ConnID uint64` with the comment "used to detect 'claim moved while my job was in flight' — late results land on /dev/null". Nothing in §4.2.0, §4.2.1, or §4.4.1 says who assigns it, when it bumps (per `OpHello`? per `attach` grant?), or how a worker compares-and-drops. This is the late-result-discard mechanism — must be specified, not implied. Either spec the lifecycle or remove the field and rely on the per-route worker invariant alone.

2. **`OnInbound` signature drift across §4.5 / §4.5.1 / §8.** §4.5 prose: `OnInbound(msg) → msg | drop`. §4.5.1 Go: `func(*Inbound) (*Inbound, bool)`. §8 table: `(msg) → (msg, drop bool)`. Pick the Go form everywhere — the prose forms read as "msg OR drop".

3. **`OnVoiceReceived` signature mismatch host vs example.** §4.5.1 declares `func(VoicePayload) (string, error)`. §8's STT example: `func handleVoice(ctx context.Context, channel string, payload VoicePayload) (string, error)`. Reconcile — recommend widening the host interface to `(ctx, channel, payload)` (matches the §4.5 table's `(chan, payload)` and gives plugins cancellation).

4. **Release-vs-in-flight ordering ambiguous.** §4.2.0 says the route worker serializes inbound + control jobs on one queue. §4.2.1 says "in-flight inbound when claim releases: the current message (already mid-deliver) completes if possible". If a release job is queued behind an in-flight STT call, does the post-STT delivery proceed against the soon-to-release stub, or get rerouted to cooldown? Pick one. Recommendation: release is a hard barrier — drain in-flight, process release, post-release inbound hits cooldown-fallback. This is what makes `ConnID` (issue 1) load-bearing.

## Tighten-up items

- **`ReplyResult` referenced but never defined.** `Channel.SendReply` returns `(*ReplyResult, error)` (§4.1) — type is undefined. Add `type ReplyResult struct{ MessageID int64 }`.
- **`AttachedMsg` has no structured `ClaimHolder`.** §5.6's collision flow surfaces "held by claude pid 12345"; only `HelloAckMsg` carries `ClaimHolder`. `AttachedMsg` has only `Err string`. Add `ClaimHolder *Holder` to `AttachedMsg` or note collisions are string-only.
- **Skeleton `mappings.json` vs strict validator.** §5.1.5 writes a skeleton on first run; §4.3 says broker exits non-zero on any parse failure. Spell out: empty `bot_token` is a warning, not a parse error — else fresh installs boot-loop.
- **`priority` direction.** §4.3 "lower runs first". §8 says "first non-drop wins" / "first non-empty wins". Confirm "first" = "lowest priority" explicitly, especially for `OnVoiceReceived`.
- **`debounce_max_messages`: §4.3 configurable, §6 reads hardcoded 50.** Cross-reference or drop the §6 number.
- **`react` tool but no `Channel.SendReaction`.** §4.1's Channel interface lists `SendReply / SendTyping / EditMessage / ValidateTopic / CreateTopic` — no reaction method. §4.4.2 keeps `react` in the harmonized tool set. Add it to the interface or note it's Telegram-channel-internal.

## Data flow trace — voice transcription returning after claim release

1. Telegram emits `Inbound{Attachments:[{Kind:"voice"}], Text:""}` for `(tg,-100…,281)`.
2. Route worker dequeues, fires `OnVoiceReceived` — STT shim spawns Python whisper. Worker is blocked per §4.2.0.
3. Mid-blocking, the claiming Claude stub's stdin EOFs. Broker's IPC reader enqueues `controlJob{release}` behind the in-flight inbound.
4. STT returns transcript after 8s. Worker substitutes `Inbound.Text`, runs `OnInbound`, debounces, looks up `ROUTES[key]`. Per §4.2.1 the message "completes if possible" — so it proceeds even though release is queued next; the stub-write fails with EPIPE and broker logs.
5. Worker then processes release, drops claim, clears placeholder + typing. Next inbound for the route hits cooldown-fallback.

**Issue:** spec admits both "complete if possible" (§4.2.1) and "one mutator per route at any instant" (§4.2.0) without naming the precedence rule when an in-flight delivery overlaps a queued release. See lock-blocker #4.

## Recommendation

**One short fix pass, then lock.** Blockers are local edits — no rework. Tighten `ConnID` lifecycle, harmonize the three hook signatures, define `ReplyResult`, fix release ordering. Do not restructure; the five-plane decomposition, per-route serial executor, and Codex env-var table are sound.
