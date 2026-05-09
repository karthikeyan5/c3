package broker

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/karthikeyan5/c3/internal/ipc"
	"github.com/karthikeyan5/c3/internal/mappings"
)

// handleAttach is the broker-side attach proposal flow per spec §5.2-§5.5.
//
// Logic:
//
//  1. Parse AttachReq.
//  2. Resolve channel (args.Channel or default).
//  3. Branch by target type:
//     a. args.Target == "dm" → claim (channel, dm_chat_id, nil), no per-cwd
//        persistence (DM is universal).
//     b. args.TopicID != nil → validate via channel.ValidateTopic + claim by
//        id; register topic in mappings.json:channels.<name>.topics with a
//        placeholder name if not already present; persist cwd mapping.
//     c. args.Name != "" or inferred from basename(cwd) → search topic
//        registry. If found in default group → claim. If found in another
//        group → propose disambiguation. If found nowhere → propose creation.
//  4. On args.Create == true → call channel.CreateTopic, register topic,
//     claim, persist mapping.
func (b *Broker) handleAttach(conn *ipc.Conn, stub *Stub, raw []byte) {
	var req ipc.AttachReq
	if err := json.Unmarshal(raw, &req); err != nil {
		_ = conn.WriteJSON(ipc.AttachedMsg{
			Op: ipc.OpAttached, OK: false,
			Err: "malformed attach: " + err.Error(),
		})
		return
	}

	chanName := req.Channel
	if chanName == "" {
		chanName = b.defaultChannel()
	}
	if chanName == "" {
		_ = conn.WriteJSON(ipc.AttachedMsg{
			Op: ipc.OpAttached, OK: false,
			Err: "no channel registered; configure mappings.json:channels.<name>",
		})
		return
	}

	// If the caller passed a freeform Expr, parse it into structured fields
	// before dispatching. This is the shared parser every CLI's slash-command
	// wrapper invokes via `attach(expr=$ARGUMENTS)` — keeps each CLI's
	// per-command file a one-liner with no duplicated parsing.
	if req.Expr != "" {
		applyExprToAttachReq(&req)
	}

	switch {
	case strings.EqualFold(req.Target, "dm"):
		b.attachDM(conn, stub, chanName, req.Steal)
	case req.TopicID != nil:
		b.attachByTopicID(conn, stub, chanName, *req.TopicID, req.Group, req.Steal)
	default:
		// Pass through the user-supplied name as-is. attachByName will
		// backfill from cwd basename only AFTER the saved-mapping check,
		// so an empty name doesn't get treated as "explicit override" of
		// the saved cwd mapping.
		if req.Name == "" && req.CWD == "" {
			_ = conn.WriteJSON(ipc.AttachedMsg{
				Op: ipc.OpAttached, OK: false,
				Err: "attach: provide cwd, name, target, or topic_id",
			})
			return
		}
		b.attachByName(conn, stub, chanName, req.Name, req.CWD, req.Group, req.Create, req.Steal)
	}
}

// applyExprToAttachReq parses the user-supplied freeform argument string and
// fills in the structured fields. Rules (documented in the AttachReq.Expr
// godoc and docs/COMMANDS.md):
//
//	""                          → leave fields untouched (cwd-saved silent claim)
//	"dm" / "DM" (case-insens)   → Target = "dm"
//	"<int>"                     → TopicID = <int>
//	"-y <name>" / "yes <name>" / "create <name>"
//	                            → Name = <name>, Create = true
//	"<anything else>"           → Name = <string>
//
// Whitespace is trimmed; unparsable input falls through to Name with the
// raw string so the broker can tell the user "no topic by that name; want
// to create?". The "create" prefix forms map to the existing Create flag —
// users who want to skip the propose/confirm round-trip type `/c3:attach
// create my-topic` and the broker creates it on the spot.
func applyExprToAttachReq(req *ipc.AttachReq) {
	expr := strings.TrimSpace(req.Expr)
	if expr == "" {
		return
	}
	if strings.EqualFold(expr, "dm") {
		req.Target = "dm"
		return
	}
	// Numeric → topic id.
	if n, err := strconv.ParseInt(expr, 10, 64); err == nil {
		v := n
		req.TopicID = &v
		return
	}
	// Create-prefixed forms.
	for _, p := range []string{"-y ", "--yes ", "yes ", "create "} {
		if strings.HasPrefix(strings.ToLower(expr), p) {
			req.Name = strings.TrimSpace(expr[len(p):])
			req.Create = true
			return
		}
	}
	req.Name = expr
}

// attachDM claims the user's 1-on-1 chat with the bot. Spec §5.5: never
// persists a per-cwd mapping; DM is universal across cwds.
//
// DM disambiguation (Karthi 2026-05-09): if a topic named "dm"
// (case-insensitive) exists in the channel, we can't tell whether the user
// meant the actual Telegram DM or that topic. Surface as needs_confirmation
// with a "disambiguate_dm" proposal — LLM asks the user. If they want the
// topic, agent re-invokes `attach name="dm"` (or topic_id); for the actual
// DM, agent re-invokes with `attach target="dm"` and a confirm flag (TBD)
// or just agrees by sending steal=true to bypass. For now: agent re-issues
// using the explicit form the user chose.
func (b *Broker) attachDM(conn *ipc.Conn, stub *Stub, chanName string, steal bool) {
	cc, ok := b.Mappings.Channels[chanName]
	if !ok || cc.DMChatID == 0 {
		_ = conn.WriteJSON(ipc.AttachedMsg{
			Op: ipc.OpAttached, OK: false,
			Err: fmt.Sprintf("attach dm: channels.%s.dm_chat_id not set in mappings.json", chanName),
		})
		return
	}

	// Disambiguation: a "dm"-named topic in the channel makes the request
	// ambiguous. Skip disambiguation if the caller already steered past it
	// by passing steal=true (which here we interpret as "I confirmed I want
	// the actual DM, just attach").
	if !steal {
		for _, tp := range cc.Topics {
			if strings.EqualFold(tp.Name, "dm") {
				_ = conn.WriteJSON(ipc.AttachedMsg{
					Op: ipc.OpAttached, OK: false,
					NeedsConfirmation: true,
					Proposal: &ipc.Proposal{
						Action:  "disambiguate_dm",
						Channel: chanName,
						Group:   tp.Group,
						Name:    tp.Name,
						Existing: &ipc.TopicEntry{
							Channel: chanName, ChatID: tp.ChatID,
							TopicID: tp.TopicID, Name: tp.Name, Group: tp.Group,
						},
					},
				})
				return
			}
		}
	}

	key := MakeRouteKey(chanName, cc.DMChatID, nil)
	if !b.tryClaim(conn, stub, key, "DM", steal) {
		return
	}
	_ = conn.WriteJSON(ipc.AttachedMsg{
		Op:      ipc.OpAttached,
		OK:      true,
		Channel: chanName,
		ChatID:  cc.DMChatID,
		Name:    "dm",
	})
}

// attachByTopicID validates a topic id against the channel (cheap typing
// action) and, if valid, claims it. Adds to topics registry as `topic-<n>`
// if not already known. Persists cwd mapping if cwd is provided.
func (b *Broker) attachByTopicID(conn *ipc.Conn, stub *Stub, chanName string, topicID int64, groupName string, steal bool) {
	cc, ok := b.Mappings.Channels[chanName]
	if !ok {
		_ = conn.WriteJSON(ipc.AttachedMsg{
			Op: ipc.OpAttached, OK: false,
			Err: fmt.Sprintf("attach: channel %q not in mappings.json", chanName),
		})
		return
	}
	gName, gCfg, ok := b.resolveGroup(cc, groupName)
	if !ok {
		_ = conn.WriteJSON(ipc.AttachedMsg{
			Op: ipc.OpAttached, OK: false,
			Err: fmt.Sprintf("attach: group %q not in mappings.json:channels.%s.groups", groupName, chanName),
		})
		return
	}
	ch, err := b.Channel(chanName)
	if err != nil {
		_ = conn.WriteJSON(ipc.AttachedMsg{Op: ipc.OpAttached, OK: false, Err: err.Error()})
		return
	}
	if err := ch.ValidateTopic(gCfg.ChatID, topicID); err != nil {
		_ = conn.WriteJSON(ipc.AttachedMsg{
			Op: ipc.OpAttached, OK: false,
			Err: fmt.Sprintf("attach --topic=%d: %v", topicID, err),
		})
		return
	}

	// Register topic in registry if absent.
	if _, exists := b.Mappings.LookupTopicByID(chanName, gCfg.ChatID, topicID); !exists {
		b.Mappings.UpsertTopic(chanName, mappings.Topic{
			ChatID: gCfg.ChatID, TopicID: topicID,
			Name: fmt.Sprintf("topic-%d", topicID), Group: gName,
		})
	}

	tid := topicID
	key := MakeRouteKey(chanName, gCfg.ChatID, &tid)
	if !b.tryClaim(conn, stub, key, fmt.Sprintf("topic %d", topicID), steal) {
		return
	}
	tp, _ := b.Mappings.LookupTopicByID(chanName, gCfg.ChatID, topicID)
	b.persistMapping(stub, chanName, gCfg.ChatID, topicID, tp.Name, gName)

	_ = conn.WriteJSON(ipc.AttachedMsg{
		Op:      ipc.OpAttached,
		OK:      true,
		Channel: chanName,
		ChatID:  gCfg.ChatID,
		TopicID: &tid,
		Name:    tp.Name,
		Group:   gName,
	})
}

// attachByName runs the full search flow per spec §5.2-§5.4:
//
//  1. If args.CWD has a saved mapping AND no explicit name was provided
//     (or the explicit name matches the saved mapping's name) → silent
//     claim of the saved route. The user can OVERRIDE the saved mapping
//     by passing an explicit name that differs.
//  2. Else search default group for `name` → if found, claim it.
//  3. Else search all groups → if found in non-default, propose
//     disambiguation (action="use_existing_other_group").
//  4. Else propose creation in default group (action="create").
//
// On any "propose" outcome the response carries needs_confirmation=true and
// a Proposal payload; the agent re-calls attach with create=true to confirm.
func (b *Broker) attachByName(conn *ipc.Conn, stub *Stub, chanName, name, cwd, groupName string, create bool, steal bool) {
	cc, ok := b.Mappings.Channels[chanName]
	if !ok {
		_ = conn.WriteJSON(ipc.AttachedMsg{Op: ipc.OpAttached, OK: false,
			Err: fmt.Sprintf("attach: channel %q not in mappings.json", chanName)})
		return
	}

	// 1. Saved mapping wins — but only if the user didn't explicitly ask
	// for a different topic. Karthi 2026-05-09: a stale cwd-mapping made
	// `attach name=c3` silently bind to topic-948 instead of c3, because
	// the saved mapping pointed at 948. Honor explicit name now.
	//
	// Note `name` is the USER-SUPPLIED name here — empty if not provided.
	// We backfill from cwd basename after this block so an empty name
	// is treated as "no explicit choice" rather than "explicit choice
	// equal to cwd basename".
	if cwd != "" {
		if m, ok := b.Mappings.LookupByCwd(cwd); ok && m.Channel == chanName {
			explicitOverride := name != "" && name != m.Name
			if !explicitOverride {
				tid := m.TopicID
				tidPtr := &tid
				if tid == 0 {
					tidPtr = nil
				}
				key := MakeRouteKey(chanName, m.ChatID, tidPtr)
				if !b.tryClaim(conn, stub, key, m.Name, steal) {
					return
				}
				b.persistMapping(stub, chanName, m.ChatID, m.TopicID, m.Name, m.Group)
				_ = conn.WriteJSON(ipc.AttachedMsg{
					Op: ipc.OpAttached, OK: true,
					Channel: chanName, ChatID: m.ChatID, TopicID: tidPtr,
					Name: m.Name, Group: m.Group,
				})
				return
			}
		}
	}

	gName, gCfg, ok := b.resolveGroup(cc, groupName)
	if !ok {
		_ = conn.WriteJSON(ipc.AttachedMsg{Op: ipc.OpAttached, OK: false,
			Err: fmt.Sprintf("attach: group %q not in mappings.json:channels.%s.groups", groupName, chanName)})
		return
	}

	// Backfill name from cwd basename if still empty (steps 2-4 need a name
	// to search/propose). At this point either no saved mapping was found,
	// or the user explicitly differs from saved.
	if name == "" && cwd != "" {
		name = filepath.Base(cwd)
	}
	if name == "" {
		_ = conn.WriteJSON(ipc.AttachedMsg{Op: ipc.OpAttached, OK: false,
			Err: "attach: provide cwd, name, target, or topic_id"})
		return
	}

	// 2. Default-group search.
	if tp, ok := b.Mappings.LookupTopicInDefaultGroup(chanName, name); ok && tp.Group == gName {
		// In the default group already — silent claim.
		tid := tp.TopicID
		key := MakeRouteKey(chanName, tp.ChatID, &tid)
		if !b.tryClaim(conn, stub, key, tp.Name, steal) {
			return
		}
		b.persistMapping(stub, chanName, tp.ChatID, tp.TopicID, tp.Name, tp.Group)
		_ = conn.WriteJSON(ipc.AttachedMsg{
			Op: ipc.OpAttached, OK: true,
			Channel: chanName, ChatID: tp.ChatID, TopicID: &tid,
			Name: tp.Name, Group: tp.Group,
		})
		return
	}

	// 3. Cross-group search.
	allHits := b.Mappings.LookupTopicAcrossGroups(chanName, name)
	otherGroupHits := allHits[:0:0]
	for _, h := range allHits {
		if h.Group != gName {
			otherGroupHits = append(otherGroupHits, h)
		}
	}
	if len(otherGroupHits) > 0 && !create {
		// Propose disambiguation. Pick first hit; the agent can disambiguate
		// further if multiple exist.
		hit := otherGroupHits[0]
		alt := &ipc.Proposal{Action: "create", Channel: chanName, Group: gName, Name: name}
		_ = conn.WriteJSON(ipc.AttachedMsg{
			Op: ipc.OpAttached, OK: false,
			NeedsConfirmation: true,
			Proposal: &ipc.Proposal{
				Action:  "use_existing_other_group",
				Channel: chanName,
				Group:   hit.Group,
				Name:    hit.Name,
				Existing: &ipc.TopicEntry{
					Channel: chanName, ChatID: hit.ChatID,
					TopicID: hit.TopicID, Name: hit.Name, Group: hit.Group,
				},
				Alternative: alt,
			},
		})
		return
	}

	// 4. Propose or perform creation.
	if !create {
		_ = conn.WriteJSON(ipc.AttachedMsg{
			Op: ipc.OpAttached, OK: false,
			NeedsConfirmation: true,
			Proposal: &ipc.Proposal{
				Action: "create", Channel: chanName, Group: gName, Name: name,
			},
		})
		return
	}
	b.createAndClaim(conn, stub, chanName, gName, gCfg.ChatID, name, cwd, steal)
}

// createAndClaim invokes channel.CreateTopic, registers the topic, claims, persists.
func (b *Broker) createAndClaim(conn *ipc.Conn, stub *Stub, chanName, gName string, chatID int64, name, cwd string, steal bool) {
	ch, err := b.Channel(chanName)
	if err != nil {
		_ = conn.WriteJSON(ipc.AttachedMsg{Op: ipc.OpAttached, OK: false, Err: err.Error()})
		return
	}
	topicID, err := ch.CreateTopic(chatID, name)
	if err != nil {
		_ = conn.WriteJSON(ipc.AttachedMsg{Op: ipc.OpAttached, OK: false,
			Err: fmt.Sprintf("create topic %q: %v", name, err)})
		return
	}
	b.Mappings.UpsertTopic(chanName, mappings.Topic{
		ChatID: chatID, TopicID: topicID, Name: name, Group: gName,
	})
	tid := topicID
	key := MakeRouteKey(chanName, chatID, &tid)
	if !b.tryClaim(conn, stub, key, name, steal) {
		return
	}
	if cwd != "" {
		b.persistMapping(stub, chanName, chatID, topicID, name, gName)
	}
	_ = b.SaveMappings()

	_ = conn.WriteJSON(ipc.AttachedMsg{
		Op: ipc.OpAttached, OK: true,
		Channel: chanName, ChatID: chatID, TopicID: &tid,
		Name: name, Group: gName,
	})
}

// tryClaim attempts to add (key → stub) to ROUTES; on collision with a
// different alive holder, sends AttachedMsg with a force_steal proposal
// (the LLM-side asks the user; on confirmation, attach is re-invoked with
// steal=true).
//
// Single-claim-per-stub invariant (Karthi 2026-05-09: "codex was attached
// to two topic IDs"): if this stub already holds a different route, that
// claim is released BEFORE the new one is granted. An adapter that wants
// to switch topics can do so with a single attach call; it will never end
// up holding two topics simultaneously.
//
// steal=true: the user has confirmed displacement of any existing holder.
// Force-release first, then claim. Only this path can evict a live PID's
// claim; everything else returns force_steal proposal for confirmation.
func (b *Broker) tryClaim(conn *ipc.Conn, stub *Stub, key RouteKey, label string, steal bool) bool {
	if old := stub.CurrentRoute(); old != nil && *old != key {
		b.Routes.Release(*old, stub.ConnID)
	}
	if steal {
		b.Routes.ForceReleaseKey(key)
	}
	holder, ok := b.Routes.Claim(key, stub)
	if !ok {
		_ = conn.WriteJSON(ipc.AttachedMsg{
			Op: ipc.OpAttached, OK: false,
			NeedsConfirmation: true,
			Proposal: &ipc.Proposal{
				Action:  "force_steal",
				Channel: key.Channel,
				Name:    label,
				Holder: &ipc.Holder{
					CLI: holder.CLI, PID: holder.PID, CWD: holder.CWD,
				},
			},
			Err: fmt.Sprintf("attach %s: held by %s pid %d (cwd %s) — re-invoke with steal=true to force",
				label, holder.CLI, holder.PID, holder.CWD),
		})
		return false
	}
	stub.SetRoute(&key)
	return true
}

// persistMapping upserts the cwd → mapping into the in-memory MappingsFile.
// SaveMappings is called at the end of any attach that mutates state to flush
// to disk atomically.
func (b *Broker) persistMapping(stub *Stub, chanName string, chatID, topicID int64, name, group string) {
	if stub.CWD == "" {
		return
	}
	now := time.Now().UTC()
	b.Mappings.UpsertMapping(stub.CWD, mappings.Mapping{
		Channel:        chanName,
		ChatID:         chatID,
		TopicID:        topicID,
		Name:           name,
		Group:          group,
		LastAttachedAt: now,
	})
	_ = b.SaveMappings()
}

// SaveMappings writes the in-memory MappingsFile to its on-disk path. Called
// after any state mutation. Best-effort — failures are logged but don't fail
// the attach (the in-memory state is what the broker uses to route).
func (b *Broker) SaveMappings() error {
	path, err := mappings.DefaultPath()
	if err != nil {
		return err
	}
	return mappings.Write(path, b.Mappings)
}

// resolveGroup returns the group name + config for the attach's group choice.
// If groupName is empty, returns the channel's default. Returns false if the
// group isn't configured.
func (b *Broker) resolveGroup(cc mappings.ChannelConfig, groupName string) (string, mappings.GroupConfig, bool) {
	if groupName == "" {
		groupName = cc.DefaultGroup
	}
	if groupName == "" {
		return "", mappings.GroupConfig{}, false
	}
	gCfg, ok := cc.Groups[groupName]
	return groupName, gCfg, ok
}

// defaultChannel returns the first channel name in mappings, or "" if none.
// In v1 there's typically only one channel (telegram), so this is unambiguous.
func (b *Broker) defaultChannel() string {
	for name := range b.Mappings.Channels {
		return name
	}
	return ""
}

