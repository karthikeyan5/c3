# C3 Channel-Capability Gaps — Root Cause, Completeness-Gate Safeguard, Coverage Matrix, and Hardened Fix Design

Date: 2026-06-16
Repo: `/home/karthi/arogara/c3`
Status: hardened design (design + Pass-1 code-grounding + Pass-2 contracts/go-nogo folded in)

This document is the single source of truth for closing the four capability gaps Karthi flagged
(expandable long-text, full polls, poll-result reading, wide-table rendering), the cross-cutting
gaps the audit surfaced, the process safeguard that prevents a recurrence, and the phase-by-phase
build plan. It supersedes the loose design + critique notes.

> **Build outcome addendum (2026-06-16) — what actually shipped vs this design.**
> This document was hardened *before* Karthi's sign-off, so its body still describes some
> capabilities as `ship-now` that he then descoped. The batch shipped as P1–P7 on `master`
> (`a59e1bb`…`a5c48f5`) plus review fixes. Resolved decisions, where the SHIPPED behavior is
> authoritative over the design text below:
> - **Q-RESULT-1 = AGGREGATE + final-on-close.** Per-voter `poll_answer` was **DESCOPED** — it
>   is **not** subscribed (not in `allowed_updates`) and **not** built. The §3 matrix row
>   "Read per-voter results (poll_answer update)" and the §4 F3.b `PollAnswer`/`PollAnswerEvent`/
>   `dispatchPollAnswer` design are therefore **DEFERRED** (kept below only as design-if-revisited);
>   per-voter reads live on the ROADMAP "build later" list, not in this batch.
> - **Q-RESULT-2 = auto-ack callbacks.** Shipped.
> - **Q-QUOTE-1 = explicit `||` terminator** (not length auto-collapse). Shipped.
> - **Q-CHUNK-1 = manifest source-budget headroom (3686) + plaintext fallback.** Shipped.
> - **Q-TABLE-1 = render-anyway + honest cross-client guidance** (we do NOT claim `<pre>` scrolls
>   on desktop/web; Android scrolls). Shipped; live scroll behavior still phone-verified.
> - **Typing-cap redesign = DEFERRED** by Karthi; `broker/worker.go` typing logic is unchanged.
> - **P7 inline keyboards = shipped now;** the other deferred gaps (albums, echo-by-`file_id`,
>   underline/mention, forwarding, location, partial-quote) were **not** pulled forward (ROADMAP).

---

## 1. Root cause — why the prior C3 channel build silently missed features

The prior build ran a multi-agent pipeline (research → design → 3 critique passes → orchestrate)
and still shipped a channel that could not expand long text, could not send full polls, could not
read poll results, and rendered wide tables as mangled literal pipe-text. None of those were
"hard" — they were *invisible*. The failure was structural in the pipeline itself, not in any one
agent's competence:

1. **Research surfaced the full surface; design quietly chose minimal-pragmatic.** The research
   pass enumerated the whole Telegram capability surface (expandable_blockquote, quiz/explanation/
   timed polls, `poll`/`poll_answer` updates, `<pre>` table rendering). The design pass then made a
   reasonable-sounding "ship the core, defer the rest" call — but the *deferral was a side effect of
   design judgment*, never an explicit, enumerated, signed-off list. "Pragmatic minimal" is the
   correct default; the bug is that the trims were invisible.

2. **Scope-trims were approved without a completeness view.** Each individual trim looked fine in
   isolation ("quiz mode is NOT implemented in v1", "StreamViaEdit:false", "Albums:false"). Nobody
   was ever shown the *aggregate* of what got trimmed against what research found. Approving N
   reasonable local trims with no global diff is exactly how a build ends up missing a whole
   category (poll reads) that everyone would have flagged if they'd seen it on one page.

3. **The 3 critique passes checked the wrong axis.** The critique passes verified (a) architecture
   soundness and (b) code-grounding (do the claimed file:line anchors and API facts hold). They did
   **not** check **feature-completeness-vs-research** — i.e. "every capability research found is
   either shipped or has an explicit, justified disposition." A design can be architecturally clean
   and perfectly code-grounded while silently omitting half the researched surface. The reviews had
   no completeness lens, so completeness was never reviewed.

4. **The orchestrator never diffed shipped-vs-researched and never surfaced the deferral list.**
   There was no gate that said "produce the list of everything research found, mark each
   ship/defer/cut, and get explicit sign-off before build." So the deferral set was never a
   first-class artifact Karthi could veto. Things fell through the gap between "researched" and
   "shipped" with nobody owning the diff.

5. **A rendering/behavior claim was asserted without live verification.** The wide-table handling
   leaned on the belief that a monospace `<pre>` block "scrolls horizontally and keeps columns
   aligned." That is **false on the most common clients** (Telegram Desktop/Win/Linux, macOS, and
   Web all *wrap* `<pre>`, breaking alignment; only Android scrolls). The claim was never live-
   verified with a real round-trip on real clients, so a wrong rendering assumption shipped as if it
   were fact. Asserting "it works" without a real round-trip is its own root cause.

**One-line root cause:** the pipeline had **no completeness gate** — research found the full
surface, design chose minimal-pragmatic, trims were locally approved without a global view, the
critiques checked architecture + code-grounding but not completeness-vs-research, the orchestrator
never diffed shipped-vs-researched nor surfaced the deferral list for sign-off, and at least one
rendering claim was asserted without a live round-trip.

---

## 2. The safeguard — the completeness gate (capability-coverage gate) in the build pipeline

The completeness gate is a process gate added to the multi-agent build pipeline so the Section-1 failure cannot
recur. It has three mechanical parts:

### 2.1 Capability-coverage matrix + explicit pre-build sign-off

After the design pass (and before any build), the pipeline **must** produce a
**capability-coverage matrix**: a row for **every capability research surfaced**, each marked
`ship-now` / `defer` / `cut` with a one-line rationale and the code-grounded current status. The
matrix is surfaced to Karthi for **explicit sign-off BEFORE build**. The deferral/cut list is now a
first-class artifact Karthi can veto. No build starts until the matrix is signed off. This directly
closes root causes 1, 2, and 4: the trims become visible, aggregated, and explicitly approved.

### 2.2 A "completeness-vs-research" lens added to the triple review

The triple review gains a **third explicit lens** alongside architecture and code-grounding:
**completeness-vs-research**. One reviewer's job is solely: "is every researched capability
accounted for in the matrix, and does the shipped set match the signed-off matrix?" This closes
root cause 3 — completeness is now a reviewed axis, not an assumed one. (In this very document the
three lenses are: the DESIGN, PASS-1 code-grounding, and PASS-2 contracts/go-nogo — and the
completeness check is the coverage matrix in Section 3.)

### 2.3 Live-verify every rendering/behavior claim — never assert without a round-trip

Any claim about how something **renders or behaves** (table scroll, expandable collapse, poll
delivery, typing visibility) must be **live-verified with a real round-trip** on a real client
before it is asserted as fact in a design or marked shippable. "I believe `<pre>` scrolls" is not
acceptable; "I sent a `<pre>` table to Desktop/macOS/Android and observed wrap vs. scroll" is. This
closes root cause 5. Where a real round-trip is impossible at design time, the claim is marked
explicitly UNVERIFIED and the gate forces a verify step into the build phase.

**Net:** the completeness gate = (matrix + pre-build sign-off) + (completeness-vs-research review lens) +
(live-verify rendering/behavior claims). It is cheap, mechanical, and attacks each root cause
directly.

---

## 3. Full capability coverage matrix

Every row from the audit (79 capabilities). Status = current code state; Rec = completeness-gate disposition.

| Capability | Category | Telegram support | C3 status | Code detail | Rec | Rationale |
|---|---|---|---|---|---|---|
| Bold | formatting | HTML `<b>`; MDv2 `*x*`; entity 'bold' | shipped | format.go:236-253 (`**`/`__`→`<b>`) | ship-now | Already shipped; no action. |
| Italic | formatting | HTML `<i>`; MDv2 `_x_`; entity 'italic' | shipped | format.go:257-274 (`*`/`_`→`<i>`) | ship-now | Already shipped. |
| Strikethrough | formatting | HTML `<s>`; MDv2 `~x~`; entity 'strikethrough' | shipped | format.go:225-233 (`~~`→`<s>`) | ship-now | Already shipped. |
| Underline | formatting | HTML `<u>`; MDv2 `__x__`; entity 'underline' | missing | format.go:29 doc: 'underline is not emitted' | defer | No standard markdown construct maps to underline; agent output is markdown. Low value vs. escaping/parse risk. Defer. |
| Spoiler | formatting | HTML `<tg-spoiler>`; MDv2 `\|\|x\|\|`; entity 'spoiler' | shipped | format.go:214-222 (`\|\|`→`<span class="tg-spoiler">`) | ship-now | Already shipped. |
| Inline code | formatting | HTML `<code>`; MDv2 `` `x` ``; entity 'code' | shipped | format.go:189-197 (`` ` ``→`<code>`) | ship-now | Already shipped. |
| Fenced code block (lang hint) | formatting | HTML `<pre><code class="language-xxx">`; MDv2 ```` ```lang ````; entity 'pre' | shipped | format.go:54-79, openFence 111-125; lang→class 70 | ship-now | Already shipped; substrate for the wide-table fix. |
| Inline link | formatting | HTML `<a href>`; MDv2 `[t](u)`; entity 'text_link' | shipped | format.go:199-211, 362-407 (depth-tracked parens), URL escaped 453-470 | ship-now | Already shipped with robust paren/escape handling. |
| Inline user mention (`tg://user?id=`) | formatting | HTML `<a href="tg://user?id=N">`; entity 'text_mention'; MDv2 `[name](tg://user?id=N)` | missing | No tg://user handling in format.go; only generic links 199-211 | defer | Pinging a specific user by id is rarely needed for a 1:1/topic relay. Defer; revisit for multi-user group relay. |
| Custom (premium) emoji | formatting | HTML `<tg-emoji emoji-id=N>`; MDv2 `![x](tg://emoji?id=N)`; entity 'custom_emoji' | missing | No tg-emoji/custom_emoji handling | cut | Requires bot to own/access premium custom emoji ids; out of scope for a text relay. Cut. |
| Plain blockquote | formatting | HTML `<blockquote>`; MDv2 `>` prefix; entity 'blockquote' | shipped | format.go:82-92 emits `<blockquote>` at 89; isBlockquote 134-136 | ship-now | Already shipped. |
| Expandable blockquote (Show-more) | formatting | HTML `<blockquote expandable>`; entity 'expandable_blockquote'; MDv2 `>`-block ending `\|\|`; vertical collapse only; still 4096-bound | **missing** | format.go:89 only emits plain `<blockquote>`; 0 'expandable' hits in non-test Go; vendored gotgbot knows the tag (formatting.go:216) but C3's converter never emits it | **ship-now** | One of Karthi's must-fix four. The only native in-message 'collapse long output' affordance; prior build silently missed it. Needs a chosen markdown trigger wired to emit `<blockquote expandable>`. Single-file, bounded. |
| HTML-significant char escaping (`< > &`) | formatting | HTML parse_mode requires `&lt; &gt; &amp;` | shipped | format.go:420-441 | ship-now | Already shipped; prevents parse 400s. |
| Lists (bullet/numbered as text) | formatting | Telegram HTML has NO list tag; flatten to text | shipped | format.go:94-99, 149-173 (degraded to `• item`/`N. item`) | ship-now | Already shipped; degradation forced by API. Correct. |
| Plaintext fallback on parse-entity 400 | formatting | API 400s on malformed HTML; no auto-recovery | shipped | outbound.go:85-94, 108-117 (resend as plain) | ship-now | Already shipped; critical robustness net. |
| entities[] array formatting path | formatting | Pass MessageEntity[] instead of parse_mode; offsets UTF-16 | missing | C3 uses parse_mode:'HTML' exclusively; no MessageEntity construction | defer | HTML path works + has fallback. entities[] is more deterministic but a larger rewrite. Defer. |
| Send text message (sendMessage, 4096) | messaging | sendMessage.text up to 4096 UTF-16; over-limit 400'd | shipped | capabilities.go:14 maxMessageRunes=4096; outbound send path | ship-now | Core path, shipped. |
| Link previews (LinkPreviewOptions) | messaging | is_disabled, url, prefer_small/large_media, show_above_text | missing | No link_preview_options on any send | defer | Defaults reasonable for a relay. Minor UX polish. Defer. |
| Message effects (message_effect_id) | messaging | Animated effect, private chats only; ids undocumented | missing | Not set anywhere | cut | Cosmetic, DM-only, ids undocumented. Cut. |
| disable_notification (silent) | messaging | Bool on nearly all sends | missing | Not set on any send path | defer | Could silence chunk floods but needs a product decision. Defer. |
| protect_content | messaging | Bool blocks forward/save | missing | Not set on any send path | defer | Niche confidentiality feature; needs a policy decision. Defer. |
| message_thread_id (forum topic) | messaging | Targets a specific forum topic | shipped | threadID(args.TopicID) on every send: media.go/sendpoll.go/text/edit/typing | ship-now | Already shipped; core to topic routing. |
| allow_paid_broadcast | messaging | Pay Stars to exceed rate limits | missing | Not set anywhere | cut | Irrelevant to a personal/low-volume relay. Cut. |
| business_connection_id | messaging | Send as a connected Business account | missing | Not set; no business_connection handling | cut | Telegram Business is out of scope. Cut. |
| reply_parameters (replies) | messaging | message_id, quote, quote_entities, quote_position, etc. | partial | replyParams(args.ReplyTo) sets message_id on every send; partial-quote fields unused; inbound quote context surfaced (inbound.go:39-45) | defer | Basic reply-to shipped. Outbound partial-quote rarely needed. Defer. |
| Send dice/dart/slot (sendDice) | messaging | server-decided value | missing | No sendDice anywhere | cut | Gimmick, no relay value. Cut. |
| Forward message (forwardMessage[s]) | messaging | keep 'Forwarded from' header | missing | No forwardMessage anywhere | cut | Relay generates/echoes; cross-chat forwarding not a use case. Cut. |
| Copy message (copyMessage) | messaging | forward without attribution; re-caption | missing | No copyMessage; echo done via file_id re-send | cut | Re-send-by-file_id covers the echo need. Cut. |
| Pin/unpin (pinChatMessage etc.) | messaging | pin/unpin/unpinAll | missing | No pin calls | defer | Could pin a summary but needs a product decision; low-frequency. Defer. |
| Inline keyboards (InlineKeyboardMarkup + callback_query) | messaging | reply_markup; callback_query update; answerCallbackQuery required | missing | No reply_markup on sends; callback_query subscribed (poll.go:17) but default-dropped (195-199); no answerCallbackQuery | defer | Genuinely useful (SSHGate approvals) but needs real design: button schema, callback routing, ack plumbing, UX decision. Defer to a dedicated interactive-controls effort (Phase 7). |
| Reply keyboards / ForceReply / RemoveKeyboard | messaging | ReplyKeyboardMarkup etc. | missing | None constructed | defer | Awkward for a free-form text agent; inline keyboards are the better primitive. Defer (subsumed). |
| Photo send | media | sendPhoto; caption 0-1024; has_spoiler; URL 10MB/upload 50MB | shipped | media.go:96-107 (SendPhoto, HasSpoiler) | ship-now | Shipped. |
| Document send | media | sendDocument; caption 0-1024; thumbnail; disable_content_type_detection | shipped | media.go:108-118 | ship-now | Shipped. |
| Video send | media | sendVideo; supports_streaming; duration/w/h; thumbnail; cover; start_timestamp | partial | media.go:119-130 sends video+caption+spoiler; no supports_streaming/metadata | defer | Basic video works. supports_streaming improves UX; metadata optional. (supports_streaming one-liner pulled into Phase 1.) |
| Audio send | media | sendAudio; duration/performer/title | shipped | media.go:131-141 | ship-now | Shipped. |
| Voice note send | media | sendVoice OGG/OPUS; duration | shipped | media.go:142-152 | ship-now | Shipped; symmetric to inbound STT. |
| Animation/GIF send | media | sendAnimation; has_spoiler | shipped | media.go:153-164 (HasSpoiler) | ship-now | Shipped. |
| Sticker send (sendSticker) | media | WEBP/TGS/WEBM; emoji; no caption | missing | No SendSticker dispatch; inbound stickers→emoji text (inbound.go:109-119) | cut | Outbound stickers have no relay value; inbound handled. Cut outbound. |
| Video note send (sendVideoNote) | media | round bubble; no URL | missing | No SendVideoNote; inbound surfaced (inbound.go:102-108) | cut | Outbound round-video not a relay need. Cut outbound. |
| Location/live location (sendLocation) | media | live_period; heading; proximity_alert; edit/stop live | missing | No SendLocation | cut | No location use case. Cut. |
| Venue (sendVenue) | media | lat/lon + title + address | missing | No SendVenue | cut | Out of scope. Cut. |
| Contact (sendContact) | media | phone + name; vcard | missing | No SendContact | cut | Out of scope. Cut. |
| Albums/media groups (sendMediaGroup) | media | media[] 2-10; mixing rule; returns Message[]; album caption on [0] | missing | Descoped v1: Albums:false (capabilities.go:51); gate one-part-per-item (gate.go:156-164); SendReply rejects >1 media (outbound.go:53-57) | defer | Real value but non-trivial: InputMedia, attach://, mixing validation, Message[]. Own scoped effort. Defer with intent. |
| Caption length enforcement (1024 UTF-16) | media | 0-1024 UTF-16 post-entities | shipped | capabilities.go:16; media.go:80-85; captionUTF16Len 19-21 | ship-now | Shipped; pre-rejects over-limit on converted length. |
| File supply by file_id (zero-cost re-send) | media | Pass inbound file_id; no size limit, no download/upload | partial | media.go:47-71 handles Path/URL; no explicit file_id passthrough | defer | Echo-by-file_id sidesteps 20MB/50MB caps. Focused enhancement. Defer. |
| has_spoiler on media | media | Bool on photo/video/animation | shipped | item.Spoiler media.go:99/122/156; dispatch.go:200-202; caps.go:117-123 | ship-now | Shipped. |
| Send poll - regular | polls | question 1-300; options 2-10; returns Message w/ poll | shipped | sendpoll.go:21-62; uses InputPollOption (36-39, modern shape) | ship-now | Shipped on modern InputPollOption (avoids legacy bare-string 400). |
| Poll anonymity (is_anonymous) | polls | default true; governs whether poll_answer ever delivered | shipped | sendpoll.go:43-45; dispatch.go:149 default true | ship-now | Shipped. To READ per-voter, must set is_anonymous=false AND subscribe poll_answer (see poll-read row). |
| Poll multiple answers (allows_multiple_answers) | polls | ignored for quiz | shipped | sendpoll.go:46; dispatch.go:150 default false | ship-now | Shipped. |
| Quiz poll (type=quiz + correct option) | polls | type='quiz'; correct option required; **live API moved to plural `correct_option_ids`, rc.34 dep is singular `CorrectOptionId`** | **missing** | sendpoll.go:14-16 doc 'Quiz mode NOT implemented in v1'; PollSpec (caps.go:127-132) has no correct-answer field — structural gap; gotgbot rc.34 exposes singular `CorrectOptionId` | **ship-now** | Part of 'full polls'. Add correct-option field to PollSpec/schema/dispatch/sendpoll. **Follow the pinned dep: use singular `CorrectOptionId` (no gotgbot bump).** |
| Quiz explanation (explanation + parse_mode) | polls | 0-200 chars shown on wrong answer | missing | SendPollOpts.Explanation never set; PollSpec lacks field | ship-now | Part of 'full polls'. Ships alongside quiz (same PollSpec expansion). |
| Timed poll (open_period / close_date) | polls | open_period seconds OR close_date Unix ts; **live API max raised to 2,628,000s; rc.34 doc still says 5-600 but enforces nothing**; mutually exclusive | missing | SendPollOpts.OpenPeriod/CloseDate never set; not in PollSpec | ship-now | Part of 'full polls'. Add open_period to PollSpec/schema. **Do NOT hard-reject 5-600 (rejects live-valid polls); enforce mutual-exclusivity with close_date only.** |
| Poll new fields (revoting/shuffle/add-options/hide-results/members-only/country/desc/media) | polls | recent additions; ranges 'confirm vs live API' | missing | None modeled or set | defer | Nice-to-have embellishments beyond Karthi's set; **not present in rc.34 SendPollOpts** (needs dep bump). Defer. |
| Stop poll (stopPoll) to read final tally | polls | returns final Poll w/ tallies; only own polls | missing | No StopPoll; no message_id retained for stopping (sendpoll.go:61 returns msg.MessageId, unstored) | ship-now | Supports 'reading poll results'. Deterministic force-close + read. Pairs with poll/poll_answer subscription. |
| Read poll aggregate results (poll update) | polls | Update.poll = Poll w/ totals + per-option counts; 'poll' must be in allowed_updates | **missing** | allowedUpdates (poll.go:17) lacks 'poll'; dispatchUpdate (189-200) default-drops it | **ship-now** | Karthi's explicit must-fix. Add 'poll' to allowedUpdates AND a dispatch case. Prior build silently missed exactly this. |
| Read per-voter results (poll_answer update) | polls | PollAnswer w/ user + option_ids; ONLY non-anon bot-sent polls; must be in allowed_updates | **missing** | allowedUpdates lacks 'poll_answer'; no PollAnswer case | defer | **Descoped by Q-RESULT-1 (2026-06-16) → aggregate-only shipped.** Per-voter "who-voted-what" is on the ROADMAP build-later list; design retained in §4 F3.b as design-if-revisited. |
| Send reaction (setMessageReaction, single) | reactions | reaction[] of ReactionType; at most one; [] removes; is_big; fixed allowed set | shipped | telegram.go:197-216; manifest Reactions:true/ReactionsSingle:true (54-55); dispatch.go:208-219; main.go:785-796 | ship-now | Shipped. |
| Reaction emoji validation | reactions | only fixed standard set valid; others 400 | missing | telegram.go:201-204 passes verbatim; no allowlist check | defer | Small robustness nicety (<50 LOC, in-package set embedded in gotgbot doc). Opportunistic — pulled into Phase 1. |
| Big/animated reaction (is_big) | reactions | plays big animated effect | missing | React never sets IsBig | cut | Cosmetic; no relay value. Cut. |
| Custom-emoji / paid reactions | reactions | ReactionTypeCustomEmoji / ReactionTypePaid | missing | React only constructs ReactionTypeEmoji | cut | Premium/paid out of scope. Cut. |
| Read inbound reactions (message_reaction) | reactions | MessageReactionUpdated; not default-delivered; bot admin in groups | partial | 'message_reaction' IS in allowedUpdates but no dispatch case — default no-op (195-199, 'Plan 8+ adds reaction hooks') | defer→fold | Real but secondary. Subscription already paid for; only routing missing. Cheap to fold into the poll-read routing work (Phase 4). |
| Read aggregate reaction counts (message_reaction_count) | reactions | MessageReactionCountUpdated; channels/large groups; not default | missing | Not in allowedUpdates; no dispatch case | defer | Only meaningful in channels/large groups; not C3's 1:1/topic model. Defer. |
| Edit message text (editMessageText) | messaging | text/parse_mode/entities/link_preview/reply_markup; 4096; 'not modified' 400 | shipped | telegram.go:158-194 (+md→HTML + fallback); EditMessages:true (56); over-limit hard-reject dispatch.go:241-244; main.go:800-811 | ship-now | Shipped. |
| Edit caption/media/reply_markup | messaging | editMessageCaption/Media/ReplyMarkup | missing | Only EditMessageText wired | defer | Text edit covers common case. Caption/media edits edge; markup needs keyboards. Defer. |
| Delete message(s) | messaging | deleteMessage; deleteMessages 1-100 | missing | No DeleteMessage | defer | Retracting a mistaken message plausibly useful but needs tool + id tracking + product decision. Defer. |
| Inbound text message | inbound | Update.message.text (default) | shipped | convertInbound inbound.go:120-122 | ship-now | Core inbound, shipped. |
| Inbound edited_message | inbound | default-delivered | shipped | poll.go:17 + dispatchUpdate 193-194 (treated as fresh) | ship-now | Shipped (fresh inbound for v1). |
| Inbound voice (STT path) | inbound | message.voice; getFile then download ≤20MB | shipped | inbound.go:51-65 (STT-prefix, empty Text until transcribe) | ship-now | Shipped; feeds whisper STT. |
| Inbound photo (best-res) | inbound | photo = PhotoSize[]; pick largest | shipped | inbound.go:66-74, pickBestPhoto 150-161 | ship-now | Shipped. |
| Inbound document/audio/video/video_note/sticker | inbound | default-delivered | shipped | inbound.go:75-83/84-92/93-101/102-108/109-119 | ship-now | Shipped; full inbound attachment coverage. |
| Inbound reply/quote context | inbound | reply_to_message + quote as context | shipped | inbound.go:39-45; SupportsReplyContext:true (72) | ship-now | Shipped. |
| Inbound album buffering (media_group_id) | inbound | N updates share media_group_id; debounce into one | partial | each item a separate inbound; MediaGroupId dedup key (dedup.go:98) is dedup not debounce-buffer | defer | A user album surfaces as N inbounds. Coalescing needs a debounce-window design. Defer; pairs with outbound albums. |
| Inbound 20MB download ceiling handling | inbound | getFile capped 20MB; link unusable larger | shipped | outbound.go:247-250; capabilities.go:20 | ship-now | Shipped; degrades gracefully. |
| Inbound callback_query (button presses) | inbound | default-delivered; must answerCallbackQuery promptly | partial | 'callback_query' IS in allowedUpdates but no dispatch case (195-199); no answerCallbackQuery; never surfaced | defer→fold | Subscribed but dropped; meaningful only once outbound keyboards exist. Fold routing into Phase 4; usefulness gated on Phase 7 keyboards + Q-RESULT-2 auto-ack. |
| Inbound service messages (forum/member/title) | inbound | fields on Message | missing | Dropped by design via isUnsupportedService (166-182) | defer | Intentionally dropped; mostly noise. Defer (current drop reasonable). |
| Inbound channel_post / business_* / chat_member / chat_join_request / chat_boost | inbound | various; some default, some need allowed_updates | missing | Not in allowedUpdates; no dispatch cases | cut | Outside C3's 1:1/topic model. Cut (revisit if group/channel relay becomes a goal). |
| Delivery transport: getUpdates long-polling | inbound | offset/limit/timeout/allowed_updates; no webhook coexist | shipped | pollLoop poll.go:19+; offset persistence 46-54,177-182; dedup 163-170 | ship-now | Shipped; correct ack-via-offset + dedup. |
| Delivery transport: setWebhook (+ secret_token) | inbound | url/max_connections/allowed_updates/drop_pending/secret_token | missing | getUpdates exclusively; no webhook server | defer | Long-polling fine for a single-host relay. Webhooks matter only at scale. Defer. |
| allowed_updates re-listing discipline | inbound | explicit list = ONLY listed deliver; must re-list everything or they silently stop | partial | allowedUpdates (poll.go:17) = {message, edited_message, callback_query, message_reaction}; OMITS poll & poll_answer → silently dropped (the documented trap) | **ship-now** | This is the mechanism behind the poll-read gap and the safeguard the matrix exists to enforce. Adding poll/poll_answer MUST extend the list in lockstep. Fix in the poll-read work. |
| Message length: 4096 (UTF-16) + chunking | limits | 4096 UTF-16/msg; over-limit rejected; entities can't span | shipped | capabilities.go:14; construct-aware chunker chunk.go (98-202, linkSpan 293-309); UTF-16 measure gate.go:197-199 & chunk.go:407-412; hard-split 375-403 | ship-now | Shipped with construct-aware splitting. Strong. |
| Chunker measures source markdown not converted HTML | limits | 4096 applies to SENT (HTML-converted) length, UTF-16 | partial | Chunker measures SOURCE markdown vs 4096 (gate.go:79-98); HTML conversion later (outbound.go:68-69); HTML-expanded chunk can 400, recovered only by plaintext fallback (85-94); captions DO measure converted (media.go:81-85) | ship-now | Narrow latent edge; fallback already recovers. Fix via source-budget headroom + fallback (Phase 5). |
| Caption length: 1024 (UTF-16) | limits | 0-1024 UTF-16 | shipped | capabilities.go:16; converted length media.go:81-85 | ship-now | Shipped. |
| Upload 50MB / download 20MB caps | limits | upload 50MB; getFile 20MB; URL photo 10MB/other 20MB | shipped | media.go:58-61 (caps 18); outbound.go:247-250 (caps 20) | ship-now | Shipped; both caps enforced with graceful degradation. |
| Send rate / flood-limit pacing | limits | ~1/s per chat, ~30/s overall, 429 retry_after | shipped | c.rate.Wait before each send (sendpoll.go:52 + media/text); recordOutboundErr | ship-now | Shipped; important when chunking into many parts. |
| Wide-table horizontal rendering | formatting | No API control over scroll vs wrap. Monospace `<pre>` is the ONLY column-alignment mechanism; **Desktop/macOS/Web WRAP (breaks alignment), Android scrolls.** Mitigations: narrow/transpose, ASCII `\| - +`, or image. expandable does NOT fix width. | **missing** | No table logic in format.go; markdown table rows go through renderInline (101-103) as literal pipe-text; only monospaced if the AGENT triple-fences it | **ship-now** | Karthi's must-fix + prior silent miss. Detect a GFM table and auto-wrap into `<pre>` so columns align; narrow/warn given desktop-wrap. **We do NOT claim `<pre>` scrolls cross-client (it does not on desktop/web).** |
| Typing relay (chat action 'typing') | other | sendChatAction 'typing' (~5s); honors thread_id | shipped | worker.go pulseTyping→SendTyping 539-553→telegram.go:122-141; Typing:true (57); arm 479-504, disarm 509-517 | ship-now | Shipped, well-architected (single-goroutine, idle-timeout primary stop). |
| Typing pulse cap tuning (maxTypingPulses) | other | re-pulse cadence is the bot's choice | shipped | maxTypingPulses=15 (worker.go:99), typingInterval=4s (91); disarm after 15 no-reply pulses ~60s (532-538) | ship-now | The 60s cliff is the real defect (loses typing on long turns). Redesign in Phase 5 — but see C5/Q-TYPING-1: broker can only observe broker-visible work. |
| send_typing tool (manual) | other | sendChatAction | shipped | dispatch.go:271-278; guidance.go:91-95 tells agent NOT to call (auto) | ship-now | Shipped; intentionally de-emphasized. |
| Streaming via edit (StreamViaEdit) | other | rapid editMessageText; rate-limited; 'not modified' 400 | missing | StreamViaEdit:false (75); guidance 98-102 reports NOT available; MinEditInterval reported (76, 1s at 24) | defer | Attractive but rate-limit-fragile + a real build. Deliberately deferred v1. Defer. |

**Counts:** 79 capabilities — 39 shipped, 9 partial, 31 missing. Recommendations: 41 ship-now,
22 defer, 16 cut. The ship-now set is dominated by already-shipped core PLUS the four Karthi
must-fixes (expandable blockquote, full polls, poll-result reading, wide tables) and the
cross-cutting `allowed_updates` re-listing fix that is the actual mechanism of the poll-read miss.

---

## 4. Hardened fix design (DESIGN + Pass-1 + Pass-2, folded clean)

### 4.0 Version realities (stated correctly — Pass-1 B1 fix)

The module pins `github.com/PaulSonOfLars/gotgbot/v2@v2.0.0-rc.34`. Provenance note (Pass-1 D6 /
Pass-2 should-fix): rc.34 was inspected from the **module cache zip**, not an extracted module
directory (the extracted dir on disk is the unrelated v1.0.0); the API facts below were verified
from that zip. Two facts the implementer must internalize:

- **Live Telegram API moved quiz to plural `correct_option_ids`; the pinned rc.34 dep is still
  singular `CorrectOptionId int64`.** We **follow the dep → use singular**, no gotgbot bump. (The
  earlier framing "the matrix is wrong about Telegram" was backwards: the matrix was right about the
  *live API*; the *dep* is stale. Conclusion unchanged: use singular.)
- **Live API `open_period` max is 2,628,000s; rc.34's doc still says 5-600 but enforces nothing**
  (plain `int64`, no validation). So a gate hardcoding a 5-600 hard-reject would **reject
  live-valid polls**. Do not do that.

Confirmed-present in rc.34: `SendPollOpts{Type string, IsAnonymous *bool, AllowsMultipleAnswers
bool, Explanation, ExplanationParseMode, ExplanationEntities, OpenPeriod int64, CloseDate int64,
IsClosed bool, CorrectOptionId int64}`; `Bot.StopPoll(chatId, messageId int64, *StopPollOpts)
(*Poll, error)`; `Update.Poll *Poll`; `Update.PollAnswer *PollAnswer` with `PollAnswer{PollId
string, VoterChat *Chat, User *User, OptionIds []int64}`; `Message.Poll *Poll`; HTML formatter maps
`expandable_blockquote → <blockquote expandable>` (formatting.go:**216**); `SendVideoOpts.
SupportsStreaming bool`; `AnswerCallbackQuery(callbackQueryId string, *AnswerCallbackQueryOpts)`;
standard reaction-emoji set embedded in `ReactionTypeEmoji.Emoji` doc (gen_types.go:8821).

### 4.0.1 Archguard constraints that bind every change

1. `bannedLiterals = {gotgbot, MarkdownV2, message_thread_id, parse_mode, sendPhoto, 4096}` matched
   by `strings.Contains(tok, lit)` on **code tokens** (comments exempt) of any file **outside**
   `internal/channel/telegram/`. So: no `parse_mode`/`gotgbot` literal in core; any width/limit
   const named/valued with `4096` as a **substring** must live in-package (telegram) or come from
   the manifest (the gate reads `c.MaxMessageRunes` with no literal). New poll/blockquote field
   names are fine (not banned); `PollKind` strings `"regular"`/`"quiz"` are fine in core; but the
   wire strings `opts.Type="quiz"` and `ExplanationParseMode="HTML"` stay in the telegram package.
2. `internal/capability` may import **only** `internal/c3types` + stdlib. All gate/guidance/chunk
   deltas obey this.
3. `internal/channel` top-level must not import gotgbot. New inbound event types are
   channel-neutral structs in `c3types`.

The design keeps the 4-layer shape: flat `Capabilities` manifest (L1) → pure `Gate`/`GuidanceFor`
(L2) → impure broker dispatch/worker (L3) → telegram channel owns all wire detail (L4).

---

### Feature 1 — Wide-table horizontal rendering

**Research finding (live-verify posture):** a `<pre>` block gives **no** API control over
horizontal scroll. Desktop (Win/Linux) + macOS + Web **wrap** `<pre>` (breaking column alignment);
**Android scrolls** (preserves it). `<blockquote expandable>` collapses **vertically only** and does
NOT add horizontal scroll. There is no parse_mode flag / entity / markup that forces cross-client
horizontal scroll. **We do NOT assert "`<pre>` scrolls and stays aligned."** This claim must be
re-confirmed by a real round-trip on Desktop+macOS+Android during Phase 6 (completeness-gate §2.3).

**Design (faithful alternative, no false promise) — all inside `internal/channel/telegram/`:**

- New file `internal/channel/telegram/table.go`:
  - `func detectTable(lines []string, i int) (rows [][]string, end int, ok bool)` — recognizes GFM
    shape: header line containing `|`, a delimiter line matching
    `^\s*\|?\s*:?-{1,}:?\s*(\|\s*:?-{1,}:?\s*)+\|?\s*$`, then ≥0 body rows. Returns trimmed cells
    (leading/trailing pipe stripped).
  - `func renderTableMono(rows [][]string) string` — per-column width = max display-width across
    rows; pad cells; join with ` | `; insert an ASCII `-+-` rule under the header (ASCII `| - +`
    only, never box-drawing — astral/inconsistent width on some clients); wrap in `<pre>` with each
    cell `escapeText`'d. Width measured in runes; documented caveat that CJK double-width cells
    won't align perfectly (acceptable, flagged).
  - In-package const `maxMonoTableWidth = 80` (a column-width budget; lives in the telegram package
    so the `4096`-substring archguard rule is moot here). Over-budget tables still render (Telegram
    won't reject); wrapping becomes the client's call.
- `mdToTelegramHTML` (format.go:51) block loop: before the `listItem` check and after fence/
  blockquote checks, add
  `if rows, end, ok := detectTable(lines, i); ok { out = append(out, renderTableMono(rows)); i = end; continue }`.
  A `<pre>` block is atomic, same tag-balance/escaping guarantees as fenced code.
- `chunk.go` (pure core): teach `splitBlocks` (chunk.go:98) to recognize a GFM pipe-table run
  (header + delimiter + body) as an **atomic block** (like fenced code/blockquote), using a pure
  detector mirroring `detectTable`'s shape test (kept in chunk.go, no telegram import). Prevents
  bisecting a table. Interacts with Feature 6 HTML-overflow (Phase 5).

**Gate/guidance:** no new manifest bool (tables are a RichText nicety). In `GuidanceFor` RichText
branch (guidance.go:24-29) append an honest line:

```
  Wide tables: rendered as a monospace block for column alignment. Telegram does NOT
  scroll wide content uniformly — desktop/web WRAP (breaking alignment), Android scrolls.
  Keep tables narrow (transpose, or fewer columns); for a truly wide table, send an image.
```

Derived from the RichText flag only → P4 anti-drift golden test passes after its expected-string
update (same phase).

---

### Feature 2 — Full polls (send)

**Type delta — `internal/c3types/caps.go` `PollSpec` (currently 127-132):**

```go
type PollKind string
const (
    PollRegular PollKind = "regular"
    PollQuiz    PollKind = "quiz"
)

type PollSpec struct {
    Question        string
    Options         []string
    Anonymous       bool
    MultipleAnswers bool
    Kind            PollKind // "" => regular (back-compat with existing callers)
    CorrectOption   *int     // 0-based; required when Kind==quiz. Pointer so 0 ≠ unset.
    Explanation     string   // 0-200; shown on a wrong quiz answer
    OpenPeriodSec   *int     // mutually exclusive with CloseDate
    CloseDateUnix   *int64   // mutually exclusive with OpenPeriod
}
```

`Kind==""` keeps every existing call site valid → regular poll.

**Gate validation — `internal/capability/gate.go` (pure), folded with Pass-2 CB-4 and Pass-1 B2/B3:**

The gate already hard-rejects polls on `!c.Polls` (gate.go:59). Add **structural poll validation**
there (pure, returns the same `err` hard-rejection shape). **Pass-2 CB-4: the existing
`len(options) < 2` reject lives in the BROKER (`dispatchPoll`, dispatch.go:138-140) — DELETE it from
dispatch and move the check to the gate** (otherwise double/divergent rejection). Update
`dispatch_test.go` which asserts the current broker message.

Validation rules — **all presented as explicit C3 POLICY, not API facts (Pass-1 B3 / Pass-2):**
- `len(Options) < 2` → reject. *Policy note:* live API now allows a 1-option poll; C3 deliberately
  requires ≥2 (a 1-option poll is meaningless for a relay). Document as policy.
- `len(Options) > 10` → reject. *Policy note:* rc.34 enforces no max; live API caps higher than 10.
  C3 caps at 10 as policy.
- `len(Question)` (rune count) `> 300` → reject (matches rc.34 + live).
- `Kind==quiz`:
  - `CorrectOption == nil` → reject ("a quiz poll requires correct_option"). **Required** — Telegram
    400s otherwise.
  - `*CorrectOption < 0 || *CorrectOption >= len(Options)` → reject (out of range).
  - `MultipleAnswers` set → **note + clear it** (degradation alteration `poll_multiple_ignored_quiz`)
    — matches Telegram's "ignored for quiz."
  - `len(Explanation)` (rune count) `> 200` → reject.
- `OpenPeriodSec != nil && CloseDateUnix != nil` → reject (mutually exclusive).
- **`OpenPeriodSec` range: do NOT hard-reject 5-600 (Pass-1 B2 — rejects live-valid 1-hour polls).**
  Either no range check (let Telegram 400 surface) or, if a ceiling is wanted, use the live ceiling
  2,628,000 via a **manifest field** `MaxPollOpenPeriodSec int` so the pure gate stays
  channel-neutral (no hardcoded Telegram limit in core). Recommended default: **no range check**;
  only enforce mutual-exclusivity. (See Q-POLL-2.)

These run in `Gate` before parts are emitted; the poll still rides a single part (gate.go:165-172).

**Channel wire — `internal/channel/telegram/sendpoll.go` (singular CorrectOptionId per dep):**

```go
opts := &gotgbot.SendPollOpts{
    IsAnonymous:           &anon,
    AllowsMultipleAnswers: spec.MultipleAnswers,
    MessageThreadId:       threadID(args.TopicID),
    ReplyParameters:       replyParams(args.ReplyTo),
    RequestOpts:           requestOptsFor("sendPoll", longPollTimeoutSeconds),
}
if spec.Kind == c3types.PollQuiz {
    opts.Type = "quiz"
    if spec.CorrectOption != nil {
        opts.CorrectOptionId = int64(*spec.CorrectOption) // SINGULAR per rc.34
    }
    if spec.Explanation != "" {
        opts.Explanation = mdToTelegramHTML(spec.Explanation)
        opts.ExplanationParseMode = "HTML" // parse_mode literal stays in-package — legal
    }
}
if spec.OpenPeriodSec != nil { opts.OpenPeriod = int64(*spec.OpenPeriodSec) }
if spec.CloseDateUnix != nil { opts.CloseDate = *spec.CloseDateUnix }
```

Retain the returned `msg.MessageId` AND `msg.Poll.Id` for Feature 3's `sentPolls` map (see 4.F3).

**MCP tool schema — `internal/mcptools/schema.go` `PollToolSchema()` (74-88):** add `type`
(enum regular/quiz), `correct_option` (integer, REQUIRED for quiz), `explanation` (0-200),
`open_period` (integer seconds; mutually exclusive with close_date), `close_date` (integer Unix ts).

**Dispatch — `internal/broker/dispatch.go` `dispatchPoll` (132-161):** **delete the existing
`len(options)<2` check (CB-4)**; build the extended `PollSpec`:

```go
Kind:          c3types.PollKind(argString(args, "type", "regular")),
CorrectOption: argIntPtr(args, "correct_option"),   // NEW helper (*int; only argInt64Ptr exists today)
Explanation:   argString(args, "explanation", ""),
OpenPeriodSec: argIntPtr(args, "open_period"),
CloseDateUnix: argInt64Ptr(args, "close_date"),
```

All validation now lives in the pure gate; dispatch just builds the spec and surfaces gate
errors/notes via `sendParts`.

**Guidance — `guidance.go` polls branch (66-70):** expand the existing `c.Polls` line to mention
regular/quiz (type="quiz" + correct_option + optional explanation), anonymity, multiple, and a
timer (open_period OR close_date; quiz ignores multiple). Still derived from `c.Polls` alone →
anti-drift holds.

---

### Feature 3 — Reading poll results (headline; + reactions + callbacks folded in)

This is the gap with the two confirmed architectural holes. The hardened design fixes both.

#### 4.F3.a — `allowed_updates` re-listing (poll.go:17)

```go
var allowedUpdates = []string{
    "message", "edited_message", "callback_query",
    "message_reaction", "poll", "poll_answer",
}
```

Must land in the **same commit** as the poll-read dispatch (Phase 4) — adding it earlier just
buffers updates that get dropped at the `default` case (poll.go:195-199).

#### 4.F3.b — Channel-neutral inbound event shape (`internal/c3types/types.go`)

Additive, back-compat (flows through `ipc.InboundMsg{Inbound c3types.Inbound}` by JSON value-copy;
zero-value `Kind==""` = today's message; `buildClaudeChannelFrame` reads only existing fields →
existing delivery untouched):

```go
type InboundKind string
const (
    InboundMessage    InboundKind = ""        // zero value = message (back-compat)
    InboundPollResult InboundKind = "poll_result"
    InboundPollAnswer InboundKind = "poll_answer"
    InboundReaction   InboundKind = "reaction"
    InboundCallback   InboundKind = "callback"
)

type Inbound struct {
    // ...existing fields...
    Kind  InboundKind  `json:",omitempty"`
    Event *InboundEvent `json:",omitempty"`
}

type InboundEvent struct {
    PollResult *PollResult
    PollAnswer *PollAnswerEvent
    Reaction   *ReactionEvent
    Callback   *CallbackEvent
}

type PollResult struct {
    PollID      string
    Question    string
    TotalVoters int
    IsClosed    bool
    Options     []PollOptionTally
}
type PollOptionTally struct { Text string; VoterCount int }

type PollAnswerEvent struct {
    PollID    string
    Voter     Sender // empty only in the anonymous case (dropped before emit)
    OptionIDs []int  // 0-based; empty = retracted
}
type ReactionEvent struct {
    MessageID int64
    Actor     Sender
    Added     []string // standard emoji added (custom/paid → see C3 decision below)
    Removed   []string
}
type CallbackEvent struct {
    CallbackID string // needed to answerCallbackQuery
    MessageID  int64
    Actor      Sender
    Data       string
}
```

All pure data, no Telegram identifiers → archguard-clean.

#### 4.F3.c — Telegram conversion + routing (`internal/channel/telegram/poll.go` dispatchUpdate 189-200)

Replace the `default` no-op with real cases (conversion stays in-package):

```go
case u.Poll != nil:            c.dispatchPollUpdate(u.UpdateId, u.Poll)
case u.PollAnswer != nil:      c.dispatchPollAnswer(u.UpdateId, u.PollAnswer)
case u.MessageReaction != nil: c.dispatchReaction(u.UpdateId, u.MessageReaction)
case u.CallbackQuery != nil:   c.dispatchCallback(u.UpdateId, u.CallbackQuery)
```

Routing:
- **`sentPolls` map** (bounded LRU, **Pass-2 D5: confirm `dedup.go` actually has a reusable
  bounded-map before claiming reuse — if not, write a small bounded map here**). On `sendPoll`,
  store `Poll.Id → {RouteKey, MessageID, OwnerUserID}` from `msg.Poll.Id` + `msg.MessageId` + the
  route owner. On a `poll`/`poll_answer` update, look up `Poll.Id`/`PollAnswer.PollId` → original
  route → set `Inbound.ChatID/TopicID` so `MakeRouteKey` lands it on the right worker.
- `message_reaction` (`MessageReactionUpdated`) carries `Chat` + `MessageId` + `User` → route
  directly.
- `callback_query`: **Pass-1 C4 — `CallbackQuery.Message` is `MaybeInaccessibleMessage` (interface),
  not `*Message`.** Type-assert to the accessible `*gotgbot.Message` for chat/thread; on the
  inaccessible variant, fall back (drop with metadata-only log, no route). Specify this explicitly.

**Reaction conversion — Pass-1 C3 (specified, not hidden):** `MessageReactionUpdated.OldReaction /
NewReaction` are `[]ReactionType` where `ReactionType` is an **interface**
(`ReactionTypeEmoji{Emoji string}`, `ReactionTypeCustomEmoji`, `ReactionTypePaid`). Building
`Added/Removed` is a **set-diff of old vs new + a type-switch** per element. **Representation
decision:** for v1, only standard `ReactionTypeEmoji` go into `Added/Removed`; custom/paid
reactions are represented as the sentinel string `"[custom]"` / `"[paid]"` (so the agent sees
"something reacted" without a meaningful emoji). Document this.

#### 4.F3.HOLES — the two Pass-2 BLOCKERS (CB-1, CB-2) — fixed here

**CB-1 (worker event path).** `BrokerHost.Emit` → `Workers.Submit(Job{Kind:JobInbound})` → the
debounce buffer → `flushInbounds` → **`mergeBatch` (worker.go:278-303) copies ONLY
Channel/ChatID/TopicID/MessageID/Sender/Timestamp/Text/Attachments/ReplyTo** — so the new
`Kind`/`Event` fields are **silently dropped** the moment an event shares a debounce window with any
other inbound, and even at batch==1 `flushInbounds` runs `hasVoice`/STT over the event. "Reuse the
Emit path, zero new IPC ops" is **NOT safe** — it data-corrupts events. (Confirmed by reading
worker.go:278-303 against HEAD.)

**Fix (smallest correct):** make the worker event-aware.
- `mergeBatch` must (a) carry `Kind`/`Event` through, and (b) **refuse to merge a non-message
  Inbound** — every event Inbound flushes **alone** (a batch never mixes a `Kind != ""` event with
  message inbounds; events are never concatenated). Concretely: in the run loop / `flushInbounds`,
  partition the batch so each `Kind != ""` Inbound is forwarded on its own, bypassing
  `hasVoice`/STT substitution and `mergeBatch`'s text-join. This is a real `worker.go` change and is
  part of Phase 4 (it was absent from the original design).

**CB-2 (DM gate-drop of aggregate poll_result).** `Broker.Gate` (pairing.go:250-252): for a private
chat (`ChatID > 0`) it calls `IsUserAllowed(in.Sender.UserID)`. An aggregate `Update.Poll` carries
**no user** → synthesized `Sender.UserID == 0` → `IsUserAllowed(0)` → **GateDrop in every DM**. The
headline read path would be dead in DMs and alive only in allowlisted groups. (Confirmed by reading
pairing.go:230-260 against HEAD.)

**Fix:** stamp the aggregate `poll_result` (and `stop_poll` result) Inbound with the **stored
route-owner UserID** from `sentPolls` (the poll was bot-initiated on an already-trusted route), so
`IsUserAllowed(owner)` passes. (Equivalently: a gate bypass for `InboundPollResult` on a route the
bot itself initiated — but stamping the owner is cleaner and keeps the allowlist authoritative.)
Per-voter `poll_answer`, `reaction`, `callback` carry a real `User` and gate normally (strangers
dropped).

**CB-3 (gate-drop logging contract).** New event dispatch helpers must replicate the metadata-only
GATE-drop logging (poll.go:229) — no content logged, strangers see nothing — to preserve the
invariant.

**Late-event-after-worker-idle (Pass-2 edge).** A tally arriving after the route's worker has
idle-exited (`w.idle`) → `Submit` returns false → dropped with an `emit DROP` log (host.go:58-60).
**Accepted for v1 and documented:** a late tally on a long-closed poll may silently vanish; use
`stop_poll` for a deterministic read.

#### 4.F3.d — anonymity rule

Telegram delivers `poll_answer` **only for non-anonymous bot-sent polls**; no send-side special
case needed. Document it; defensively, in `dispatchPollAnswer` if `PollAnswer.User == nil`
(anonymous/voter_chat case) drop with a metadata-only log rather than emit an empty-voter event.

#### 4.F3.e — stopPoll (force-close + read final tally)

- `internal/channel/channel.go` `Channel` interface: add
  `StopPoll(chatID, messageID int64) (*c3types.PollResult, error)`. Telegram-specific in v1 (like
  `CreateTopic`); future channels stub with an unsupported error.
- `internal/channel/telegram/stoppoll.go`: `c.bot.StopPoll(chatID, messageID, &gotgbot.StopPollOpts{})`
  → convert `*gotgbot.Poll` → `c3types.PollResult`.
- New MCP tool **`stop_poll {message_id}`** → `dispatchStopPoll` → `ch.StopPoll` → format tally as
  MCP text. Schema `StopPollToolSchema()` in `mcptools/schema.go`, guarded by `caps.Polls`. Tool
  switch case in `dispatch.go:19-35`. The stamped-owner fix (CB-2) applies to the synthesized
  result if it is surfaced as an event; the direct tool return is plain MCP text (no gate).
- Passive results: the agent does NOT need to poll — `poll`/`poll_answer` arrive as `<channel>`
  events; `stop_poll` is the explicit force-close-and-read path.

#### 4.F3.f — surfacing to the agent (the `<channel>` event)

`buildClaudeChannelFrame` (main.go:508-575) sends `content` (string) + `meta`
(Record<string,string>). **`handleInbound` (main.go:457) branches on `in.Inbound.Kind`** (Pass-2:
the frame currently reads only existing fields, so new branches are genuinely required):
- `poll_result`: `content = "Poll results: «Q» — 7 votes — A:3 B:4 (closed)"`,
  `meta{kind:"poll_result", poll_id, total_voters, is_closed}`.
- `poll_answer`: `content = "@user voted: A, C"` (or "retracted vote"),
  `meta{kind:"poll_answer", poll_id, user, option_ids}`.
- `reaction`: `content = "@user reacted 👍 to message 123"`,
  `meta{kind:"reaction", message_id, ...}`.
- `callback`: `content = "@user pressed [Approve] (data=approve:42)"`,
  `meta{kind:"callback", callback_id, message_id, data}`.
- All `meta` values **stringified** (frame contract requires string meta). The one-shot wire-dump
  diagnostic (`firstInbound`, main.go:476) applies.
- Codex adapter (`cmd/c3-codex-adapter/main.go`) gets the parallel branch.

**Callback auto-ack (Q-RESULT-2, blocker for interactive buttons).** `answerCallbackQuery` exists
(gen_methods.go:68). Proposal: the channel **auto-acks every callback immediately** (empty
`answerCallbackQuery`) on receipt, then surfaces the event for async agent action — otherwise the
LLM round-trip blows the few-second ack budget and the user's button spins. Needs Karthi's nod
(changes UX: no "loading…" toast).

#### 4.F3.guidance — manifest-driven new InboundCaps fields

Add to `InboundCaps` (caps.go:62): `DeliversPollResults`, `DeliversPollAnswers`,
`DeliversReactions`, `DeliversCallbacks` (bool). Set true in the Telegram manifest. `GuidanceFor`
emits lines derived purely from these bools (P4 golden test stays honest), e.g. "Poll results:
delivered automatically as `<channel>` events (aggregate tallies; per-voter votes ONLY for
non-anonymous polls). Use `stop_poll` to force-close and read the final tally."

---

### Feature 4 — Expandable show-more blockquote

gotgbot renders `<blockquote expandable>` (formatting.go:**216**). We emit it directly (server-
rendered HTML — no client capability question; still verify the collapse round-trip in Phase 3).

**Trigger (explicit construct, not auto-collapse):** a `>`-prefixed blockquote run whose **last
line ends with `||`** → expandable. Predictable + testable; piggybacks on the existing blockquote
run detection (format.go:82-92).

**`format.go` change (82-92):** while collecting the run, detect if the final quoted line's stripped
content ends with `||`; if so, strip the trailing `||` and emit `<blockquote expandable>...
</blockquote>`; else plain `<blockquote>`. Helper `isExpandableBlockquoteEnd(line string) bool` +
trim. Tag-balance preserved (opens+closes in-function).

**Chunker interaction (chunk.go):** blockquote runs are already atomic (137-148), never bisected.
The matrix note "still 4096-bound" holds: an expandable quote that alone exceeds the limit hits the
existing `hard_split` path (174-184) and emits the existing `hard_split` note ("a blockquote
exceeded the message limit and was hard-split — formatting may be affected"). **Pass-2 edge:** if
`hard_split` bisects the run, the `||` may land mid-run and render literally on one part — covered
weakly by that note; acceptable for v1. The detector groups by `>` prefix regardless of terminator
→ no chunker change needed.

**Manifest/guidance:** add `ExpandableQuotes bool` to `Capabilities`, true in Telegram manifest.
`GuidanceFor` (RichText branch) adds: "For a long quoted block that should collapse behind a 'Show
more' chevron, end the blockquote with a line containing only `||` (still capped at the message
length limit)."

---

### Feature 5 — Typing-cap tuning + chunk HTML-overflow guard

**Pass-1 C5 / Pass-2 C5 — the motivating scenario is mis-analyzed; the premise must be dropped.**
A local **Bash tool call never reaches the broker** (it is not a c3 MCP tool), so `resetIdle()`
never fires during a long bash. A `lastRealWorkAt = resetIdle-time` watchdog would **also** expire
during a silent 3-minute bash — it does **not** fix the cited scenario. The broker can only observe
**broker-visible** activity. The original redesign's claim to both preserve the no-idle-forever
invariant **and** keep typing alive through a silent long bash is **internally contradictory**.

**Hardened decision — pick ONE honest model (Q-TYPING-1):**

- **Option A (recommended): accept broker-visible-only tracking and raise/remove the 60s cliff
  honestly.** The current code (worker.go:74-99, 528-538) is already correct on the invariant; the
  only true defect is the fixed 60s `maxTypingPulses` cliff. Replace the pulse-count cap with a
  `typingMaxSilence` watchdog driven by `lastRealWorkAt` (set inside `resetIdle()` — single source
  of truth with the idle timer; a typing tick NEVER touches it, so a tick can't extend its own
  life). `typingMaxSilence = min(w.idle, 90s)`. **Honest scope:** typing stays alive across
  broker-visible work (multi-tool turns), stops on reply, stops on true broker-visible silence; it
  **will** stop during a long fully-silent local bash — and that is documented as a known limit, not
  papered over.
- **Option B: wall-clock re-pulse decoupled from real-work** — keeps typing alive through a silent
  bash but **reintroduces the idle-forever risk** the cap guards. Not recommended.

**Do not ship the contradictory both-at-once premise.** Concrete changes (Option A): remove
`typingPulses`/`maxTypingPulses` (worker.go:79,99,532-538); set `lastRealWorkAt = time.Now()` inside
`resetIdle()` (151-159); arm/re-arm (479-504; forwardOrFallback 365; dispatchOutbound non-reply
463-465) reset `lastRealWorkAt`; disarm on reply (462), worker exit (deferred 125), SendTyping error
(549-553), and `time.Since(lastRealWorkAt) > typingMaxSilence` in `pulseTyping`. Keep the loud
disarm log.

**Chunk HTML-overflow guard (gap #6).** The pure chunker measures **source markdown** vs 4096
(gate.go:79-98); HTML conversion happens later (outbound.go:68-69) and can push a near-limit chunk
over 4096 → 400, recovered only by the plaintext fallback (85-94). The pure capability package
cannot run the HTML converter (archguard) and cannot hold a `4096`-substring literal.
**Recommended v1 (Q-CHUNK-1): manifest-field headroom + fallback.** Add a manifest field
`MaxMessageRunesSource int` (a conservative chunking budget slightly below the hard 4096) read by
the gate (the gate already reads `c.MaxMessageRunes` with no literal — **PITFALL A: never put a
`4096`-containing literal in core; it comes from the manifest**). Converted HTML then rarely exceeds
4096; the plaintext fallback remains the safety net. Captions already measure converted length
(media.go:81-85); the text path now matches in spirit. (Alternative: a true in-channel HTML-aware
re-splitter in `outbound.go` — more code, fully correct — deferred.)

---

### 4.5 newlySurfacedGaps — disposition (ship-now folded in; defers flagged)

| # | Gap | Disposition |
|---|---|---|
| 1 | allowed_updates omits poll/poll_answer (poll.go:17) | Fixed in F3.a, lockstep with poll-read dispatch (Phase 4). |
| 2 | callback_query subscribed, never surfaced; no answerCallbackQuery | Folded into F3.c (`InboundCallback` + dispatch + `MaybeInaccessibleMessage` handling) + auto-ack (Q-RESULT-2). |
| 3 | Outbound inline keyboards absent (no reply_markup) | Phase 7 (own feature); prerequisite for callbacks to be useful (Q-RESULT-3). |
| 4 | Inbound reactions partial (subscribed, no dispatch) | Folded into F3.c (`InboundReaction` + set-diff/type-switch conversion, C3). |
| 5 | stopPoll unimplemented; sent poll message_id not retained | F3.e + `sentPolls` map retains `(pollID → route, msgID, owner)`. |
| 6 | Chunker measures source vs 4096; HTML expansion can 400 | Phase 5: manifest-field headroom + plaintext fallback (PITFALL A). |
| 7 | Inbound albums not coalesced; missing outbound sendMediaGroup | Deferred — flagged. Q-ALBUM-1. |
| 8 | Reaction emoji not validated before send | Phase 1 opportunistic (<50 LOC, in-package allowed set from gotgbot doc). |
| 9 | sendVideo omits supports_streaming + metadata | Phase 1: `SupportsStreaming: true` one-liner; metadata deferred (Q-VIDEO-1). |
| 10 | Echo-by-file_id not wired | Deferred — flagged. Q-ECHO-1. |
| 11 | Outbound partial-quote unused | Deferred — flagged. Q-QUOTE-2. |
| 12 | Modern poll embellishments (shuffle/hide-results/revoting) | Deferred — NOT in rc.34 SendPollOpts (needs dep bump). Q-POLL-3. |
| 13 | Underline / user-mention / custom-emoji no emitter | Deferred — flagged. Q-FMT-1. |

### 4.6 Outbound inline keyboards (gap #3) — sketch for Phase 7 (Karthi go/no-go)

If Q-RESULT-3 = ship: add `Outbound.Buttons [][]Button` (`Button{Text, Data string}`, neutral) to
`c3types`; gate passes through; `SendReply`/`sendMedia`/`sendPoll` build
`gotgbot.InlineKeyboardMarkup` (in-package); MCP `reply` schema gains a `buttons` arg. This is what
makes Feature 3's callbacks actionable (SSHGate-style approve/deny). Largest net-new surface; own
phase.

### 4.7 Key file:line anchors

- Manifest: caps.go:127 (`PollSpec`), :62 (`InboundCaps`), :17 (`Capabilities`).
- Pure gate: gate.go:59 (poll reject — add validation), :79-98 (source chunk), reads
  `c.MaxMessageRunes` (no literal).
- Chunker: chunk.go:98-166 (`splitBlocks` — add table recognition), :137 (blockquote atomic).
- Guidance: guidance.go:24-29 (RichText), :66-70 (polls).
- Format: format.go:51 (block loop — table + expandable hooks), :82-92 (blockquote).
- Poll send: sendpoll.go:44-61 (opts mapping + retain pollID/msgID/owner).
- allowed_updates + routing: poll.go:17, :189-200 (dispatchUpdate cases), :212-243 (gate+emit +
  GATE-drop logging to replicate).
- Outbound wire: outbound.go:42 (SendReply HTML overflow), :197 (React validation),
  media.go:119-130 (sendVideo streaming).
- Dispatch: dispatch.go:19-35 (tool switch — add stop_poll), :132-161 (`dispatchPoll` — DELETE the
  `<2` check), :384 (`argInt64Ptr`; add `argIntPtr`).
- Worker: worker.go:79,99 (remove pulse cap), :151-159 (`resetIdle` — set `lastRealWorkAt`),
  :190-208 (debounce/run loop — event-aware path), :278-303 (`mergeBatch` — carry Kind/Event +
  refuse to merge events), :479-538 (arm/disarm/pulse).
- Host/gate: host.go:53 (`Emit`), pairing.go:250-252 (DM allowlist — stamp owner for poll_result).
- Adapter: main.go:457-575 (`handleInbound`/frame — event branches), :476 (firstInbound dump),
  :820-826 (poll tool reg). Schemas: mcptools/schema.go:74 (`PollToolSchema`), add
  `StopPollToolSchema`.
- Archguard (must stay green): archguard/guard_test.go — bannedLiterals includes 4096/parse_mode/
  gotgbot; capability import purity.

---

## 5. Phase-by-phase build plan

Each phase ends green: `go build ./...`, `go test ./...`, and the **archguard tests**
(`internal/archguard`) pass. New behavior gets unit tests; the P4 golden guidance test's expected
string is updated **in the same phase** that changes guidance. Every rendering/behavior claim gets a
**live round-trip verify** (completeness-gate §2.3) in its phase.

**Phase 1 — Opportunistic in-channel fixes (no core changes).**
Files: `internal/channel/telegram/outbound.go` (reaction-emoji allowed-set validation in `React`,
gap #8), `internal/channel/telegram/media.go` (`SupportsStreaming: true` on sendVideo, gap #9) +
tests. *(Per "obvious fixes: just do" — <50 LOC single-file mechanical; do not gate on Karthi.)*
Verify: send a disallowed reaction → clear error not raw 400; send a video → streaming playback.

**Phase 2 — Full polls (send).**
Files: `internal/c3types/caps.go` (`PollSpec` + `PollKind`), `internal/capability/gate.go` (poll
validation — folds Pass-1 B2/B3 + Pass-2 CB-4), `internal/broker/dispatch.go` (`dispatchPoll` —
**delete the `<2` check**, add `argIntPtr`), `internal/broker/dispatch_test.go` (update the moved
assertion), `internal/channel/telegram/sendpoll.go` (singular `CorrectOptionId`),
`internal/mcptools/schema.go` (`PollToolSchema`), `internal/capability/guidance.go` +
`guidance_test.go`. Self-contained; no inbound changes. Gated on Q-POLL-2 before merge.
Verify: send a quiz poll (correct_option + explanation) and a timed poll from the agent; observe on
a real client.

**Phase 3 — Expandable blockquote.**
Files: `internal/channel/telegram/format.go` (detector/emitter), `internal/c3types/caps.go`
(`ExpandableQuotes`), `internal/capability/guidance.go` + `guidance_test.go`,
`internal/channel/telegram/format_test.go`. Independent of polls. Gated on Q-QUOTE-1.
Verify (live round-trip): a `||`-terminated quote shows the 'Show more' chevron and collapses.

**Phase 4 — Reading poll results + reactions + callbacks (the routing phase).**
Files: `internal/channel/telegram/poll.go` (allowed_updates F3.a + dispatchUpdate cases + conversion
helpers + `sentPolls`), `internal/c3types/types.go` (`InboundKind`/`InboundEvent`/event structs),
`internal/broker/worker.go` (**CB-1 event-aware path: mergeBatch carries Kind/Event + refuses to
merge events; events bypass STT**), `internal/broker/pairing.go` (**CB-2: stamp route-owner UserID
on aggregate poll_result so DMs don't gate-drop**), `internal/channel/channel.go` (`StopPoll`),
`internal/channel/telegram/stoppoll.go`, `internal/broker/dispatch.go` (`dispatchStopPoll` + tool
switch), `internal/mcptools/schema.go` (`StopPollToolSchema`), `internal/c3types/caps.go`
(`InboundCaps` delivery bools), `internal/channel/telegram/capabilities.go` (set them true),
`cmd/c3-claude-adapter/main.go` + `cmd/c3-codex-adapter/main.go` (`handleInbound` event branches),
`internal/capability/guidance.go` + tests. **Largest phase.** Gated on Q-RESULT-1 and Q-RESULT-2
before merge — but the poll-read + reaction parts can land first and callbacks split into **Phase 4b**
if the auto-ack UX decision is deferred. **Verify: send a poll, vote from a phone, confirm the agent
receives the `poll`/`poll_answer` event IN A DM (not just a group) — this is the CB-2 regression
test; also `stop_poll` returns the final tally.** Tests-pass is NOT sufficient here (CB-1/CB-2 can
be green-but-dead) — the DM round-trip is the real gate.

**Phase 5 — Typing-cap redesign + chunk HTML-overflow guard.**
Files: `internal/broker/worker.go` (Option-A silence watchdog; remove pulse cap),
`internal/c3types/caps.go` (`MaxMessageRunesSource`), `internal/capability/gate.go` (use the
headroom budget), `internal/channel/telegram/capabilities.go` (set the field), tests. Both
correctness/robustness; independent of features 1-4. Gated on Q-TYPING-1 and Q-CHUNK-1.
Verify: a multi-tool turn keeps typing alive; a long silent bash disarms (documented limit); a
near-limit HTML-expanding chunk no longer 400s.

**Phase 6 — Wide tables.**
Files: `internal/channel/telegram/table.go` (new), `internal/channel/telegram/format.go` (hook),
`internal/capability/chunk.go` (atomic-table recognition), `internal/capability/guidance.go` +
tests, `internal/channel/telegram/format_test.go`. Last (most rendering-judgment-heavy; benefits
from Phase 5 chunk work). Gated on Q-TABLE-1. **Verify (live round-trip, mandatory — completeness-gate §2.3):
send a wide table to Telegram Desktop, macOS, and Android; confirm the wrap-vs-scroll reality and
that the guidance line is honest. We are NOT claiming `<pre>` scrolls.**

**Phase 7 — (optional, Karthi-approved only) Inline keyboards** (gap #3), enabling actionable
callbacks; plus any deferred gaps Karthi pulls forward (albums #7, echo-by-file_id #10, partial-quote
#11, poll embellishments #12, underline/mention #13).

**Ordering rationale:** Phases 1-3 are pure additive sends, no inbound risk, any order. Phase 4 is
the one that touches the live `allowed_updates` list + inbound routing + the two architectural holes
→ isolated, one reviewable unit, DM round-trip is the gate. Phase 5 fixes two latent correctness
edges. Phase 6 (tables) sits last by design dependency. Phase 7 opt-in.

---

## 6. Open decisions for Karthi (genuine product calls)

- **Q-POLL-1** Bump gotgbot past rc.34 for plural `correct_option_ids`? (Recommend NO — singular is
  sufficient; a bump risks unrelated API churn.)
- **Q-POLL-2** `open_period` validation: no range check (recommended) vs. live ceiling 2,628,000 via
  a manifest field. (Do NOT hardcode 5-600 — it rejects live-valid polls.)
- **Q-POLL-3** Ship shuffle/hide-results/revoting now? (Recommend defer — not in rc.34.)
- **Q-RESULT-1** Surface every per-voter `poll_answer` live, or only aggregates? (Per-voter can be
  chatty for a busy poll.)
- **Q-RESULT-2** Auto-ack `callback_query` immediately on receipt (no agent-in-the-loop spinner)?
  Confirm the UX (no "loading…" toast). Blocker for interactive buttons.
- **Q-RESULT-3** Ship outbound inline keyboards (gap #3) in this batch (Phase 7), or after?
- **Q-QUOTE-1** Expandable blockquote via explicit `||` terminator (recommended) vs. length
  auto-collapse?
- **Q-TYPING-1** Confirm Option A (broker-visible-only typing; `typingMaxSilence = min(w.idle, 90s)`;
  typing WILL stop during a long silent local bash — documented limit) vs. Option B (wall-clock
  re-pulse, reintroduces idle-forever risk). Recommend A.
- **Q-CHUNK-1** HTML-overflow: manifest-field source-budget headroom + plaintext fallback
  (recommended) vs. a true in-channel HTML-aware re-splitter?
- **Q-TABLE-1** Wide table over the mono budget: render-anyway + honest guidance (recommended) vs.
  auto-transpose 2-col vs. suggest-image. Also confirm: we are NOT claiming `<pre>` scrolls
  cross-client (it does not on desktop/web).
- **Q-ALBUM-1 / Q-ECHO-1 / Q-VIDEO-1 (video metadata) / Q-QUOTE-2 / Q-FMT-1** — deferred gaps
  (7/10/9-metadata/11/13): pull any into Phase 7?
- **C3-reaction representation** — confirm the v1 choice to render custom/paid reactions as a
  sentinel (`"[custom]"`/`"[paid]"`) rather than dropping them silently.
