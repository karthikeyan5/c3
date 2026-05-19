# Telegram Bot Setup for c3 — Source-of-Truth Research

**Date:** 2026-05-18
**Purpose:** Master reference for c3's install flow. Answers the bot-setup questions raised by the 2026-05-15 and 2026-05-16 install pilots.

---

## Bottom line (TL;DR for the install flow)

c3's Telegram bot needs to (a) read every group message, (b) send messages into topics, (c) create/rename/close topics. There are exactly **two** reliable ways to grant (a):

1. **Promote the bot to admin** — bot admins always receive every message regardless of privacy mode.
2. **Disable privacy mode in BotFather** (`/setprivacy → Disable`) — then re-add the bot to the group.

(1) is required anyway for sending messages into a "messages-locked" group and for `can_manage_topics`. So in practice the canonical c3 setup is **(1) plus also (2) as belt-and-braces**, because the c3 bot is always promoted to admin — the question is only whether the user actually granted the admin role correctly. (2) makes the bot work even if the user fumbled admin promotion.

---

## 1. Canonical numbered setup checklist

Each step is labeled with the **screen / client / setting** so the install flow can render it verbatim.

### Phase A — Create the bot (any client; do this in Telegram Desktop)

1. **BotFather → `/newbot`.** Open a chat with `@BotFather` in Telegram. Send `/newbot`. Pick a display name, then a username ending in `bot`. Copy the HTTP API token (`1234567:abcdefg…`). [core.telegram.org/bots/features]
2. **BotFather → `/setprivacy → <your bot> → Disable`.** Critical. This makes the bot able to read all non-bot messages in groups even if admin promotion is misconfigured. Default is `Enabled`. [core.telegram.org/bots/features#privacy-mode]
3. **BotFather → `/setjoingroups → <your bot> → Enable`.** This is the default for new bots, but verify. If it's disabled, the bot cannot be added to any group. [core.telegram.org/bots/features]
4. (Optional, recommended.) BotFather → `/mybots → <your bot> → Bot Settings → Group Admin Rights → toggle on `Manage Topics`, `Delete Messages`, `Pin Messages`. This pre-checks those boxes whenever someone promotes this bot to admin. Saves a step in Phase C.

### Phase B — Prepare the group (use Telegram Desktop, iOS, Android, or macOS — NOT Telegram Web)

5. **Create or pick a supergroup.** Forum topics require a supergroup. A new "basic group" auto-upgrades to a supergroup the first time you enable Topics, exceed the basic-group member cap, or make it public. [core.telegram.org/api/forum, core.telegram.org/api/channel]
6. **Group profile → Edit → toggle "Topics" on.** Path differs by client:
   - **Telegram Desktop (tdesktop, Win/Linux/macOS):** right-click group in sidebar → **Manage group → Topics** toggle. (Available in Telegram Desktop ≥ 4.5 / late-2022.)
   - **Telegram iOS:** group header → **Edit → Topics** toggle.
   - **Telegram Android:** group header → pencil icon → scroll to **Topics** toggle. (Android client ≥ 9.0 / late-2022.)
   - **Telegram macOS (App Store):** group header → **Info → Edit → Topics** toggle.
   - **Telegram Web (web.telegram.org/k or /a):** the Topics enable toggle is historically **missing or unreliable**; bug-tracker reports going back to 2022 confirm Web lags behind on Topics-related UI. Use a different client for this step. [bugs.telegram.org/c/23132, bugs.telegram.org/c/23731]
7. **Add the bot to the group.** Group profile → Add member → search the bot's `@username`.
8. **Promote the bot to admin.** Group profile → Administrators → Add Administrator → pick the bot. Required admin rights:
   - **Send Messages** (implied: must NOT be restricted by group default permissions while bot is non-admin; admin status overrides)
   - **Delete Messages** (so the bot can clean up its own old prompts if needed)
   - **Pin Messages** (used when topics surface a pinned summary)
   - **Manage Topics** — *the load-bearing one*. Without this, `createForumTopic` / `editForumTopic` / `closeForumTopic` all fail with `CHAT_ADMIN_REQUIRED` or `can_manage_topics` errors. [core.telegram.org/bots/api#promotechatmember, core.telegram.org/constructor/chatAdminRights]
   - Everything else off.
9. **Sanity test.** From a personal account, send a message into the General topic. From the c3 broker, verify the message arrives. Then have c3 create a topic and reply into it.

### Phase C — If privacy-mode flip was deferred until after the bot was already in the group

10. **Re-add the bot.** Privacy-mode changes only take effect after the bot is removed and re-added to the group. Kick the bot, then re-add. [core.telegram.org/bots/features#privacy-mode]

---

## 2. Common pitfalls (the four questions, answered)

### Q1. Why does `/setprivacy → Disable` fire for some pilots and not others?

**Answer:** Privacy mode is **enabled by default for every new bot**, full stop. The reason Karthi never hit it is the *admin-override exception*: **"bot admins always receive all messages regardless of the privacy mode setting"** [core.telegram.org/bots/features#privacy-mode, core.telegram.org/bots/faq]. Karthi's bots were promoted to admin and so silently ignored the privacy-mode default. A pilot who added the bot as a regular member — or who promoted it but missed the right checkboxes — falls back to the default, and the bot then sees only messages mentioning `@botname` or replies to the bot.

**Safe default for c3 install:** always have the user run `/setprivacy → Disable` during install. Even though admin status overrides it, disabling privacy is a cheap belt-and-braces fix that turns a class of "the bot isn't reading my messages" support requests into zero. The install flow should not assume the user actually promoted the bot correctly.

### Q2. Canonical minimum settings for a c3 bot

Three layers — minimum set:

| Layer | Setting | Value |
|---|---|---|
| BotFather (per-bot) | `/setprivacy` | **Disabled** |
| BotFather (per-bot) | `/setjoingroups` | **Enabled** (default) |
| BotFather (per-bot, optional) | Group Admin Rights defaults | Manage Topics, Delete Messages, Pin Messages |
| Group (per-chat) | Type | **Supergroup** (basic groups auto-upgrade when Topics is enabled) |
| Group (per-chat) | Topics | **On** |
| Group (per-chat) | Bot's group role | **Administrator** |
| Bot admin rights | Send / Delete Messages | On |
| Bot admin rights | Pin Messages | On |
| Bot admin rights | **Manage Topics** | **On** (required for forum topic create/edit/close) |
| Bot admin rights | Everything else (Ban Users, Add Admins, Anonymous, Manage Video Chats, Post/Edit/Delete Stories) | Off |

Bot admins reading messages does **not** require any specific admin checkbox — admin status alone overrides privacy mode. There is no `can_read_messages` flag. [core.telegram.org/bots/api#promotechatmember, core.telegram.org/constructor/chatAdminRights]

### Q3. Per-client matrix

Verified against authoritative Telegram bug-tracker reports and Telegram's own forum/admin docs. Where SEO blog content is the only source, that's noted.

| Setting | Telegram Desktop (tdesktop) | Telegram iOS | Telegram Android | Telegram macOS (App Store) | Telegram Web K (web.telegram.org/k) | Telegram Web A (web.telegram.org/a) |
|---|---|---|---|---|---|---|
| Enable "Topics" group toggle | Yes | Yes (some past gaps on small groups, since fixed [bugs.telegram.org/c/23731]) | Yes | Yes | **Unreliable / missing** [bugs.telegram.org/c/23132] | **Unreliable / missing** [bugs.telegram.org/c/23132] |
| Promote bot to admin | Yes (full checkbox list) | Yes (full) | Yes (full, in current versions) | Yes (full) | Partial — bug reports note admin-rights UI is incomplete in Web | Partial — same |
| "Manage Topics" admin checkbox | Yes | Yes | Yes (in current versions; legacy/pre-Android-9.x builds have been reported merging Pin+Topics into one row — community-source claim, not officially confirmed) | Yes | **Often missing** | **Often missing** |
| "Allow create topics" group permission (default for regular members) | Yes | Yes | Yes | Yes | Often missing | Often missing |
| BotFather actions (`/setprivacy`, `/setjoingroups`) | Yes (chat-based, works everywhere) | Yes | Yes | Yes | Yes | Yes |

**Recommendation:** the install flow should tell users: **"Do the group-admin and Topics-toggle steps in Telegram Desktop, iOS, Android, or macOS — NOT in Telegram Web."** BotFather steps can be done anywhere because BotFather is just a chat bot.

The General telegram.org/help and core.telegram.org docs do not enumerate per-client UI paths; the per-client paths in step 6 above are reconstructed from a mix of community sources and direct UI inspection and should be re-verified once per major client release.

### Q4. Outdated client effects

**Authoritative answer:** Telegram has not published a per-feature client-version cutoff matrix. Their general policy is "update to the latest version." What is documented:

- Forum topics were announced **2022-11-05** [telegram.org/blog/topics-in-groups-collectible-usernames]. Pre-2022-11 clients on any platform will not show the Topics UI at all.
- The `manage_topics` admin right was added to the Bot API at the same time. Clients that pre-date this will not surface a "Manage Topics" admin checkbox.
- An iOS bug where the Topics toggle was missing in small basic groups was reported and marked fixed on the Telegram bug tracker [bugs.telegram.org/c/23731].
- Community reports describe **legacy Android builds (pre-9.0)** displaying only five admin checkboxes with Pin/Topics merged, but this is **not officially documented by Telegram**.

**Don't invent cutoffs.** For c3 install, just tell the user: "If you don't see a 'Manage Topics' checkbox when promoting the bot, update your Telegram app to the latest version, then try again. If you still don't see it, switch to Telegram Desktop." That's the safe and accurate guidance.

---

## 3. What we know we don't know

- **Exact minimum Telegram client version per feature.** Telegram doesn't publish a feature/version matrix. Bug-tracker breadcrumbs exist; an authoritative table does not.
- **Whether `/setprivacy → Disable` is *strictly* unnecessary when the bot is admin** — official docs say admin overrides privacy mode, and that matches Karthi's experience. But the c3 install flow should still set it: zero-cost protection against an end-user who toggles admin off later or who never finishes the admin promotion step.
- **Telegram Web Topics-management gap.** Multiple bug reports (some marked old/outdated) say Web K and Web A both lag on Topics UI. Telegram has signaled the Web client is in maintenance mode relative to Desktop/mobile. Hard to give a single "Web client version cutoff" — recommendation stays "don't use Web for setup."
- **Current minimum-members threshold for Topics.** Originally 200 → lowered to 100 → bug-tracker comments suggest now available in any supergroup. There is no official statement at the URLs we checked confirming the current floor. Since c3 install creates a fresh dedicated group, this rarely matters; if Topics doesn't appear, member count is the likely cause and the user can either invite more members or, more commonly, just retry — basic groups auto-upgrade on Topics-enable in most cases.
- **Legacy-Android "5 admin checkboxes with Pin+Topics merged"** — circulating in community blog posts but we could not verify it on bugs.telegram.org or core.telegram.org. Flagged as community-source only.

---

## Sources

**Authoritative (Telegram official):**
- [Telegram Bot Features — Privacy Mode](https://core.telegram.org/bots/features#privacy-mode)
- [Telegram Bot Features — BotFather commands](https://core.telegram.org/bots/features)
- [Telegram Bots FAQ](https://core.telegram.org/bots/faq)
- [Telegram Bot API — promoteChatMember](https://core.telegram.org/bots/api#promotechatmember)
- [Telegram Bot API — full reference](https://core.telegram.org/bots/api)
- [Forums (MTProto-level) — supergroup requirement, manage_topics](https://core.telegram.org/api/forum)
- [chatAdminRights constructor — all admin flags](https://core.telegram.org/constructor/chatAdminRights)
- [Channels, supergroups, gigagroups, basic groups](https://core.telegram.org/api/channel)
- [Topics in Groups announcement (2022-11-05)](https://telegram.org/blog/topics-in-groups-collectible-usernames)

**Telegram bug tracker (semi-authoritative — user reports with occasional staff replies):**
- [bugs.telegram.org/c/23132 — Topics broken in Telegram Web](https://bugs.telegram.org/c/23132)
- [bugs.telegram.org/c/23731 — Topics toggle missing in small basic groups (iOS)](https://bugs.telegram.org/c/23731)
- [bugs.telegram.org/c/21766 — Topics in groups under 200 members](https://bugs.telegram.org/c/21766/12)
- [bugs.telegram.org/c/23320 — Manage Topics permission enforcement](https://bugs.telegram.org/c/23320/8)
- [bugs.telegram.org/c/4003 — Web K missing features list](https://bugs.telegram.org/c/4003/10)
- [bugs.telegram.org/c/4004 — Web A missing features list](https://bugs.telegram.org/c/4004)

**Community sources (per-client UI paths, version cutoffs — treat as informed-guess, verify before quoting):**
- TeleMe blog — [group privacy mode of Telegram bots](https://www.teleme.io/articles/group_privacy_mode_of_telegram_bots?hl=en)
- DEV Community — [Creating a private Telegram chatbot](https://dev.to/sarafian/creating-a-private-telegram-chatbot-2n9)
- Chatimize — [How to add a bot to a Telegram group](https://chatimize.com/telegram-bot-group/)
