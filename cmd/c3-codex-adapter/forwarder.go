package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/gorilla/websocket"
	"github.com/karthikeyan5/c3/internal/c3types"
)

// errCodexForwardNoWS signals that forwarding is enabled but no app-server WS URL
// is configured, so the forward delivered NOTHING. It is returned (not nil) so the
// caller does NOT ack: acking a no-op forward would let the broker consume the
// queued copy of a message the agent never received live — a silent drop. It is a
// distinct sentinel (not a generic error) so the caller can stay quiet about this
// benign (mis)configuration instead of logging a "failure" per inbound.
var errCodexForwardNoWS = errors.New("codex forward: no app-server WS URL configured (C3_CODEX_APP_SERVER_WS unset)")

type codexForwardConfig struct {
	WSURL    string
	ThreadID string
	CWD      string
	Timeout  time.Duration
}

type codexWSClient struct {
	conn    *websocket.Conn
	nextID  int
	timeout time.Duration
}

func forwardInboundToCodexAppServer(ctx context.Context, in *c3types.Inbound, cfg codexForwardConfig) error {
	if cfg.WSURL == "" {
		return errCodexForwardNoWS
	}
	if cfg.Timeout <= 0 {
		cfg.Timeout = 15 * time.Second
	}
	dialer := websocket.Dialer{HandshakeTimeout: cfg.Timeout}
	conn, _, err := dialer.DialContext(ctx, cfg.WSURL, nil)
	if err != nil {
		return fmt.Errorf("dial codex app-server: %w", err)
	}
	defer conn.Close()

	client := &codexWSClient{conn: conn, timeout: cfg.Timeout}
	if _, err := client.request(ctx, "initialize", map[string]any{
		"clientInfo": map[string]any{
			"name":    "c3-codex-bridge",
			"title":   "C3 Codex bridge",
			"version": adapterVersion,
		},
		"capabilities": map[string]any{
			"experimentalApi": true,
			"optOutNotificationMethods": []string{
				"item/agentMessage/delta",
				"item/reasoning/textDelta",
				"item/reasoning/summaryTextDelta",
			},
		},
	}); err != nil {
		return err
	}
	if err := client.notify("initialized", nil); err != nil {
		return err
	}

	threadID := cfg.ThreadID
	if threadID == "" {
		threadID, err = client.discoverThread(ctx, cfg.CWD)
		if err != nil {
			return err
		}
	}
	if threadID == "" {
		return fmt.Errorf("no loaded Codex thread found")
	}
	if _, err := client.request(ctx, "thread/resume", map[string]any{
		"threadId":     threadID,
		"excludeTurns": true,
	}); err != nil {
		return err
	}
	_, err = client.request(ctx, "turn/start", map[string]any{
		"threadId": threadID,
		"input": []map[string]any{{
			"type":          "text",
			"text":          formatInboundTurnText(in),
			"text_elements": []any{},
		}},
	})
	return err
}

func (c *codexWSClient) discoverThread(ctx context.Context, cwd string) (string, error) {
	loadedResp, err := c.request(ctx, "thread/loaded/list", map[string]any{"limit": 20})
	if err != nil {
		return "", err
	}
	loaded := stringSlice(loadedResp["data"])
	if len(loaded) == 0 {
		return "", nil
	}
	if len(loaded) == 1 {
		return loaded[0], nil
	}

	listResp, err := c.request(ctx, "thread/list", map[string]any{
		"limit":          50,
		"sortKey":        "updated_at",
		"sortDirection":  "desc",
		"cwd":            cwd,
		"useStateDbOnly": true,
	})
	if err != nil {
		return "", err
	}
	loadedSet := map[string]bool{}
	for _, id := range loaded {
		loadedSet[id] = true
	}
	if threads, ok := listResp["data"].([]any); ok {
		for _, raw := range threads {
			thread, ok := raw.(map[string]any)
			if !ok {
				continue
			}
			id := fmt.Sprint(thread["id"])
			if loadedSet[id] {
				return id, nil
			}
		}
	}
	return loaded[0], nil
}

func (c *codexWSClient) request(ctx context.Context, method string, params map[string]any) (map[string]any, error) {
	c.nextID++
	id := c.nextID
	msg := map[string]any{"id": id, "method": method}
	if params != nil {
		msg["params"] = params
	}
	if err := c.conn.WriteJSON(msg); err != nil {
		return nil, fmt.Errorf("%s: write: %w", method, err)
	}
	deadline := time.Now().Add(c.timeout)
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		_ = c.conn.SetReadDeadline(deadline)
		var resp map[string]any
		if err := c.conn.ReadJSON(&resp); err != nil {
			return nil, fmt.Errorf("%s: read: %w", method, err)
		}
		if gotID, ok := numericID(resp["id"]); ok && gotID == id {
			if rawErr, ok := resp["error"]; ok && rawErr != nil {
				encoded, _ := json.Marshal(rawErr)
				return nil, fmt.Errorf("%s: %s", method, encoded)
			}
			if result, ok := resp["result"].(map[string]any); ok {
				return result, nil
			}
			return map[string]any{}, nil
		}
		if _, hasID := resp["id"]; hasID {
			if _, hasMethod := resp["method"]; hasMethod {
				_ = c.conn.WriteJSON(map[string]any{
					"id": resp["id"],
					"error": map[string]any{
						"code":    -32601,
						"message": "c3 codex bridge does not handle app-server requests",
					},
				})
			}
		}
	}
}

func (c *codexWSClient) notify(method string, params map[string]any) error {
	msg := map[string]any{"method": method}
	if params != nil {
		msg["params"] = params
	}
	if err := c.conn.WriteJSON(msg); err != nil {
		return fmt.Errorf("%s: write notify: %w", method, err)
	}
	return nil
}

// formatInboundTurnText renders one inbound as a Codex turn. It carries the same
// information density as the queued (fetch_queue) renderQueuedInbound — message_id,
// the full reply context, and an attachment summary — so a message forwarded live
// loses none of the metadata a message read via fetch_queue would carry (D-RC1).
// The reply/attachment formatting is shared with renderQueuedInbound via the
// c3types helpers so the two can't drift.
func formatInboundTurnText(in *c3types.Inbound) string {
	thread := "0"
	if in.TopicID != nil {
		thread = strconv.FormatInt(*in.TopicID, 10)
	}
	sender := in.Sender.Username
	if sender == "" {
		sender = strconv.FormatInt(in.Sender.UserID, 10)
	}
	header := []string{fmt.Sprintf("chat=%d", in.ChatID), "thread=" + thread}
	if in.MessageID != 0 {
		header = append(header, fmt.Sprintf("message_id=%d", in.MessageID))
	}
	header = append(header, c3types.ReplyContextFields(in.ReplyTo)...)
	out := fmt.Sprintf("Telegram message from %s (%s)\n%s", sender, strings.Join(header, " "), in.Text)
	if len(in.Attachments) > 0 {
		atts := make([]string, 0, len(in.Attachments))
		for _, att := range in.Attachments {
			atts = append(atts, c3types.AttachmentField(att))
		}
		out += "\n" + strings.Join(atts, " ")
	}
	return out
}

func stringSlice(v any) []string {
	items, ok := v.([]any)
	if !ok {
		return nil
	}
	out := make([]string, 0, len(items))
	for _, item := range items {
		out = append(out, fmt.Sprint(item))
	}
	return out
}

func numericID(v any) (int, bool) {
	switch x := v.(type) {
	case float64:
		return int(x), true
	case int:
		return x, true
	case json.Number:
		n, err := x.Int64()
		return int(n), err == nil
	default:
		return 0, false
	}
}

func codexForwardConfigFromEnv() codexForwardConfig {
	cwd := os.Getenv("C3_CODEX_CWD")
	if cwd == "" {
		cwd, _ = os.Getwd()
	}
	return codexForwardConfig{
		WSURL:    os.Getenv("C3_CODEX_APP_SERVER_WS"),
		ThreadID: os.Getenv("C3_CODEX_THREAD_ID"),
		CWD:      cwd,
		Timeout:  15 * time.Second,
	}
}
