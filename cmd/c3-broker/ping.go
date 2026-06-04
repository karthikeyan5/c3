package main

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/karthikeyan5/c3/internal/ipc"
)

// runPing is the `c3-broker ping` subcommand. Sends an OpPingThisSession
// to the running broker; the broker dispatches a one-shot "this is me"
// reply to whichever Telegram route the calling session currently
// holds (matched by CWD against the live stub registry).
//
// Usage:
//
//	c3-broker ping
//
// The matching slash command is /c3:ping (plugins/c3/commands/c3-ping.md).
// TODO #19(b) — Karthi 2026-05-18.
//
// Matching is PID-primary (FIX 1, 2026-06-03): we pass our best-effort
// guess at the calling CLI session's PID (walked up the PPID chain via
// bestEffortSessionPID — the same helper /c3:sessions uses) so the broker
// can match the user's actual adapter stub even when `claude` was launched
// from a parent dir and this slash command runs from a project subdir
// (CWD-equality matching can never bridge that gap). CWD is still sent as
// the broker's fallback for when the PPID walk fails (PID==0).
func runPing(_ []string) error {
	cwd, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("getwd: %w", err)
	}
	pid := bestEffortSessionPID()
	conn, err := dialBroker()
	if err != nil {
		return err
	}
	defer conn.Close()

	if err := conn.WriteJSON(ipc.PingThisSessionReq{
		Op:  ipc.OpPingThisSession,
		PID: pid,
		CWD: cwd,
	}); err != nil {
		return fmt.Errorf("write ping_this_session: %w", err)
	}
	raw, err := conn.ReadFrame()
	if err != nil {
		return fmt.Errorf("read ping_this_session_reply: %w", err)
	}
	var resp ipc.PingThisSessionReplyMsg
	if err := json.Unmarshal(raw, &resp); err != nil {
		return fmt.Errorf("parse ping_this_session_reply: %w", err)
	}
	if !resp.OK {
		return fmt.Errorf("%s", resp.Err)
	}
	fmt.Printf("Sent identification message to %s → %s.\n", resp.Channel, resp.Topic)
	if resp.SentText != "" {
		fmt.Printf("\n%s\n", resp.SentText)
	}
	return nil
}
