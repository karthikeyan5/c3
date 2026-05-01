# karthi-setup — Karthi's portable Claude-Code setup

Drop-in setup files Karthi uses on every machine he works from. Mirrored under the C3 repo so cloning C3 onto a fresh machine pulls this with it. Once C3 is cloned, point Claude at this folder and ask it to apply the setup.

## How Karthi invokes this

On a fresh machine:

```
mkdir -p ~/arogara
git clone <c3-repo-url> ~/arogara/c3
cd ~/arogara
claude
```

Then in the Claude session:

> Go look at `~/arogara/c3/karthi-setup/` and set up my current directory as per that.

Claude reads this README and runs the steps in order.

## What this folder contains

```
karthi-setup/
├── README.md                          # this file — Claude follows it
├── arogara/
│   ├── CLAUDE.md                      # → ~/arogara/CLAUDE.md
│   └── PERSONA.md                     # → ~/arogara/PERSONA.md
└── skills/
    └── initiate-handoff/
        └── SKILL.md                   # → ~/.claude/skills/initiate-handoff/SKILL.md
```

Three things ship: the arogara-root CLAUDE.md (Ram identity + output modes + C3 project/topic conventions + multi-part-reply protocol + handoff trigger), the PERSONA.md (Ram's character), and the `/initiate-handoff` skill (durable session-pause protocol).

C3 itself (the Telegram multiplexer) has its own setup at [`../INSTALL.md`](../INSTALL.md). This folder does NOT install C3 — it sets up the arogara workspace conventions that ride on top of C3. Step 4 below points at the C3 install if it's not already done.

## Steps for Claude to follow

When Karthi says "set up my current directory as per karthi-setup" (or similar), execute these steps in order. **Show each step's command before running it; pause after step 1 to confirm directory; otherwise proceed.**

### Step 1 — Confirm the target arogara root

The setup assumes `~/arogara/` is the workspace root (where CLAUDE.md and PERSONA.md will live, with C3 + projects underneath). Verify:

```bash
pwd
```

Expected output: `<HOME>/arogara` (e.g. `/home/<user>/arogara`).

If `pwd` is NOT `~/arogara`, ask Karthi where the arogara root should be before continuing. If `pwd` IS `~/arogara`, proceed.

### Step 2 — Install the arogara CLAUDE.md and PERSONA.md

```bash
cp ~/arogara/c3/karthi-setup/arogara/CLAUDE.md   ~/arogara/CLAUDE.md
cp ~/arogara/c3/karthi-setup/arogara/PERSONA.md  ~/arogara/PERSONA.md
```

If these files already exist (Karthi resetting an existing setup), confirm with him before overwriting — these may have local edits.

### Step 3 — Install the `/initiate-handoff` skill

Skills live at `~/.claude/skills/<skill-name>/SKILL.md`.

```bash
mkdir -p ~/.claude/skills/initiate-handoff
cp ~/arogara/c3/karthi-setup/skills/initiate-handoff/SKILL.md \
   ~/.claude/skills/initiate-handoff/SKILL.md
```

After install, the skill should appear in Claude Code's available-skills list. Trigger it with the slash command `/initiate-handoff`, or whenever Karthi says phrases like "wind down", "park this", "checkpoint", "I'm about to clear context".

### Step 4 — Verify (or initiate) C3 install

The arogara CLAUDE.md assumes C3 is up — Telegram routing, topic auto-attach on session start, the `attach` / `topics` MCP tools all depend on it. Check:

```bash
test -S /tmp/c3.sock && echo "C3 running" || echo "C3 NOT running"
ls ~/.claude/channels/telegram/.env >/dev/null 2>&1 && echo "C3 configured" || echo "C3 NOT configured"
```

If either reports NOT, walk Karthi through `~/arogara/c3/INSTALL.md` — Steps 1–9. The high-level flow:

1. Install upstream Anthropic Telegram plugin (`/plugin marketplace add claude-plugins-official`, `/plugin install telegram@claude-plugins-official`).
2. Configure bot token at `~/.claude/channels/telegram/.env`.
3. Bootstrap `~/.claude/channels/telegram/access.json` with Karthi's user-id allowlist.
4. Copy `.mcp.json.example` → `.mcp.json` and replace `<ABSOLUTE-PATH-TO-c3-CLONE>` with the actual clone path.
5. Copy `mvp/config.json.example` → `mvp/config.json` (group_chat_id can stay placeholder until first group is approved).
6. Install C3 plugin from the local marketplace (`/plugin marketplace add ~/arogara/c3/plugin`, `/plugin install c3-telegram@c3`).
7. Disable the upstream Telegram plugin in `/plugin` (and verify it persisted in `~/.claude/settings.json`).
8. Add `"channelsEnabled": true` to `~/.claude/settings.json`.
9. (Optional) STT keys at `~/.claude/stt.env` for voice transcription.

If C3 IS up and configured, skip the install — just confirm to Karthi.

### Step 5 — Confirm completion

Tell Karthi which steps ran, which were skipped, and what state the machine is now in. Suggested format:

```
Setup complete:
✅ ~/arogara/CLAUDE.md installed
✅ ~/arogara/PERSONA.md installed
✅ ~/.claude/skills/initiate-handoff/ installed
<C3 status from step 4>

Next: cd ~/arogara/<project> and run claude. Auto-attach kicks in.
```

If anything failed or required Karthi's input mid-run, surface that explicitly.

## Maintenance

- **When the arogara CLAUDE.md or PERSONA.md changes** in `~/arogara/`, copy the change back into `c3/karthi-setup/arogara/` and commit so the next machine gets it. The files in this folder are the canonical source for new installs; the live files at `~/arogara/` are local copies that may drift between syncs.
- **When the `/initiate-handoff` skill changes** at `~/.claude/skills/initiate-handoff/SKILL.md`, copy the change back into `c3/karthi-setup/skills/initiate-handoff/` and commit.
- **Add new shippable elements** under `karthi-setup/` as needed (additional skills, additional setup files), updating this README's "What this folder contains" + "Steps for Claude to follow" sections in the same commit.

## Why this lives in C3

C3 is the only repo guaranteed to be on every machine Karthi works from (it's the Telegram routing layer; no C3 = no setup). Mirroring `karthi-setup/` here means cloning C3 also clones the workspace conventions. No separate dotfiles repo to remember.
