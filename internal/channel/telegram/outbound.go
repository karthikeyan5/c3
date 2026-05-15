package telegram

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"github.com/PaulSonOfLars/gotgbot/v2"

	"github.com/karthikeyan5/c3/internal/c3types"
)

// SendReply sends a text message (chunked at Telegram's 4096-char limit).
// Returns the message_id of the FIRST chunk sent. Honors message_thread_id
// when args.TopicID is non-nil. Honors reply_parameters when args.ReplyTo is
// non-nil.
//
// Files are not yet implemented in this method — when args.Files is non-empty,
// the method returns an error. Photo/document sending lands in a follow-up.
func (c *Channel) SendReply(args c3types.ReplyArgs) (int64, error) {
	if c.bot == nil {
		return 0, errors.New("telegram: channel not started")
	}
	if len(args.Files) > 0 {
		return 0, errors.New("telegram: file attachments not yet implemented (Phase 4B-followup)")
	}

	// Markdown rendering: when the caller didn't pin a ParseMode, treat the
	// text as standard markdown and convert to Telegram HTML. Without this,
	// `**bold**` and `` `code` `` show as literal characters in Telegram
	// (2026-05-09 photo report). We chunk the RAW text first, then
	// convert each chunk independently so a 4096-char split never bisects an
	// opened tag.
	autoHTML := args.ParseMode == ""
	chunks := chunkText(args.Text, 4096)
	var firstID int64
	for i, chunk := range chunks {
		opts := &gotgbot.SendMessageOpts{}
		if args.TopicID != nil {
			opts.MessageThreadId = *args.TopicID
		}
		if autoHTML {
			chunk = mdToTelegramHTML(chunk)
			opts.ParseMode = "HTML"
		} else if args.ParseMode != "" {
			opts.ParseMode = args.ParseMode
		}
		// Reply-to applies only to the first chunk; subsequent chunks chain
		// to the previous chunk implicitly via Telegram's UI.
		if i == 0 && args.ReplyTo != nil {
			opts.ReplyParameters = &gotgbot.ReplyParameters{
				MessageId:                *args.ReplyTo,
				AllowSendingWithoutReply: true,
			}
		}
		opts.RequestOpts = requestOptsFor("sendMessage", longPollTimeoutSeconds)
		if err := c.rate.Wait(c.ctx, args.ChatID); err != nil {
			return firstID, fmt.Errorf("telegram: rate-wait: %w", err)
		}
		msg, err := c.bot.SendMessage(args.ChatID, chunk, opts)
		if err != nil && autoHTML && isParseEntityError(err) {
			// Plaintext fallback (per OpenClaw bot/delivery.send.ts pattern).
			// Our markdown converter occasionally produces malformed HTML for
			// pathological input; re-send the ORIGINAL chunk as plain text
			// rather than dropping the message.
			c.host.Logf("telegram: HTML parse error on chunk %d, retrying as plaintext: %v", i, err)
			plainOpts := *opts
			plainOpts.ParseMode = ""
			plain := chunks[i] // original markdown, pre-conversion
			msg, err = c.bot.SendMessage(args.ChatID, plain, &plainOpts)
		}
		if err != nil {
			c.recordOutboundErr(err)
			if i == 0 {
				return 0, fmt.Errorf("telegram: SendMessage chunk 0: %w", err)
			}
			// Mid-chunk failure: log and stop; first chunk's ID is what we return.
			c.host.Logf("telegram: SendMessage chunk %d failed: %v", i, err)
			break
		}
		if i == 0 {
			firstID = msg.MessageId
		}
	}
	c.recordOutboundSuccess()
	return firstID, nil
}

// isParseEntityError returns whether a SendMessage error indicates Telegram
// rejected the entities we sent (malformed HTML or MarkdownV2). On these we
// retry plain-text rather than drop the message — pattern from OpenClaw's
// extensions/telegram/src/bot/delivery.send.ts (sub-agent research 2026-05-09).
func isParseEntityError(err error) bool {
	if err == nil {
		return false
	}
	s := err.Error()
	return strings.Contains(s, "can't parse entities") ||
		strings.Contains(s, "parse entities") ||
		strings.Contains(s, "find end of the entity") ||
		strings.Contains(s, "Bad Request: can't parse")
}

// SendTyping sends a typing chat action. Used both for the typing indicator
// (spec §7.1) and as the validate_topic primitive (spec §6 — sending a typing
// action with a thread_id implicitly validates the thread exists).
func (c *Channel) SendTyping(chatID int64, threadID *int64) error {
	if c.bot == nil {
		return errors.New("telegram: channel not started")
	}
	opts := &gotgbot.SendChatActionOpts{
		RequestOpts: requestOptsFor("sendChatAction", longPollTimeoutSeconds),
	}
	if threadID != nil {
		opts.MessageThreadId = *threadID
	}
	if err := c.rate.Wait(c.ctx, chatID); err != nil {
		return fmt.Errorf("telegram: rate-wait: %w", err)
	}
	if _, err := c.bot.SendChatAction(chatID, "typing", opts); err != nil {
		c.recordOutboundErr(err)
		return fmt.Errorf("telegram: SendChatAction: %w", err)
	}
	c.recordOutboundSuccess()
	return nil
}

// EditMessage edits a previously-sent message's text. Used by the
// edit_progress tool (spec §7.2) and by the broker's placeholder lifecycle.
func (c *Channel) EditMessage(args c3types.EditArgs) (*c3types.EditResult, error) {
	if c.bot == nil {
		return nil, errors.New("telegram: channel not started")
	}
	opts := &gotgbot.EditMessageTextOpts{
		ChatId:      args.ChatID,
		MessageId:   args.MessageID,
		RequestOpts: requestOptsFor("editMessageText", longPollTimeoutSeconds),
	}
	if args.ParseMode != "" {
		opts.ParseMode = args.ParseMode
	}
	if err := c.rate.Wait(c.ctx, args.ChatID); err != nil {
		return nil, fmt.Errorf("telegram: rate-wait: %w", err)
	}
	if _, _, err := c.bot.EditMessageText(args.Text, opts); err != nil {
		c.recordOutboundErr(err)
		return nil, fmt.Errorf("telegram: EditMessageText: %w", err)
	}
	c.recordOutboundSuccess()
	return &c3types.EditResult{MessageID: args.MessageID}, nil
}

// React sets a single-emoji reaction on a message.
func (c *Channel) React(args c3types.ReactArgs) error {
	if c.bot == nil {
		return errors.New("telegram: channel not started")
	}
	opts := &gotgbot.SetMessageReactionOpts{
		Reaction: []gotgbot.ReactionType{
			gotgbot.ReactionTypeEmoji{Emoji: args.Emoji},
		},
		RequestOpts: requestOptsFor("setMessageReaction", longPollTimeoutSeconds),
	}
	if err := c.rate.Wait(c.ctx, args.ChatID); err != nil {
		return fmt.Errorf("telegram: rate-wait: %w", err)
	}
	if _, err := c.bot.SetMessageReaction(args.ChatID, args.MessageID, opts); err != nil {
		c.recordOutboundErr(err)
		return fmt.Errorf("telegram: SetMessageReaction: %w", err)
	}
	c.recordOutboundSuccess()
	return nil
}

// DownloadAttachment fetches a Telegram file by file_id and saves it to a
// local cache dir. Returns the local path. Bot API caps at 20MB.
//
// Local cache layout:
//
//	$XDG_CACHE_HOME/c3/telegram/attachments/<file_unique_basename>
//	~/.cache/c3/telegram/attachments/<...>  (fallback)
func (c *Channel) DownloadAttachment(fileID string) (string, error) {
	if c.bot == nil {
		return "", errors.New("telegram: channel not started")
	}
	f, err := c.bot.GetFile(fileID, &gotgbot.GetFileOpts{
		RequestOpts: requestOptsFor("getFile", longPollTimeoutSeconds),
	})
	if err != nil {
		c.recordOutboundErr(err)
		return "", fmt.Errorf("telegram: GetFile: %w", err)
	}
	if f.FilePath == "" {
		return "", errors.New("telegram: GetFile returned empty file_path (file may be too large or expired)")
	}

	cacheDir, err := attachmentsCacheDir()
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(cacheDir, 0700); err != nil {
		return "", fmt.Errorf("telegram: mkdir cache: %w", err)
	}

	// Local filename: keep the file_unique_id stable across redownloads + the
	// upstream basename for human-friendliness.
	base := filepath.Base(f.FilePath)
	localPath := filepath.Join(cacheDir, fmt.Sprintf("%s_%s", f.FileUniqueId, base))
	if _, err := os.Stat(localPath); err == nil {
		return localPath, nil // cached
	}

	// The download URL contains the bot token; we never include it in
	// any error or log line. The relative file path is enough for
	// debugging.
	filePath := strings.TrimPrefix(f.FilePath, "/")
	dlURL := fmt.Sprintf("https://api.telegram.org/file/bot%s/%s", c.cfg.BotToken, url.PathEscape(filePath))
	req, err := http.NewRequestWithContext(c.ctx, http.MethodGet, dlURL, nil)
	if err != nil {
		return "", fmt.Errorf("telegram: build download request for %q: %w", filePath, err)
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("telegram: download %q: %w", filePath, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("telegram: download %q: HTTP %d", filePath, resp.StatusCode)
	}

	out, err := os.OpenFile(localPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return "", fmt.Errorf("telegram: create %s: %w", localPath, err)
	}
	defer out.Close()
	if _, err := io.Copy(out, resp.Body); err != nil {
		_ = os.Remove(localPath)
		return "", fmt.Errorf("telegram: copy to %s: %w", localPath, err)
	}
	return localPath, nil
}

// CreateTopic creates a new forum topic. Spec §6: rate-limit handling honors
// parameters.retry_after but does NOT silently retry on 429 — instead it
// surfaces the error so the agent can tell the user. Bulk topic creation is
// not a supported flow.
func (c *Channel) CreateTopic(chatID int64, name string) (int64, error) {
	if c.bot == nil {
		return 0, errors.New("telegram: channel not started")
	}
	if err := c.rate.Wait(c.ctx, chatID); err != nil {
		return 0, fmt.Errorf("telegram: rate-wait: %w", err)
	}
	t, err := c.bot.CreateForumTopic(chatID, name, &gotgbot.CreateForumTopicOpts{
		RequestOpts: requestOptsFor("createForumTopic", longPollTimeoutSeconds),
	})
	if err != nil {
		c.recordOutboundErr(err)
		return 0, fmt.Errorf("telegram: CreateForumTopic %q: %w", name, err)
	}
	c.recordOutboundSuccess()
	return t.MessageThreadId, nil
}

// ValidateTopic confirms a topic exists by sending a transient typing action.
// On a real topic this fires a brief typing indicator; on an invalid one
// Telegram returns 400.
func (c *Channel) ValidateTopic(chatID int64, threadID int64) error {
	return c.SendTyping(chatID, &threadID)
}

// chunkText splits a string into <=maxLen chunks at byte boundaries. UTF-8
// safety: we only break at byte boundaries that aren't continuation bytes.
func chunkText(s string, maxLen int) []string {
	if len(s) == 0 {
		return []string{""}
	}
	if len(s) <= maxLen {
		return []string{s}
	}
	var out []string
	for len(s) > maxLen {
		// Walk back from maxLen to find a non-continuation byte.
		cut := maxLen
		for cut > 0 && (s[cut]&0xC0) == 0x80 {
			cut--
		}
		if cut == 0 {
			cut = maxLen // give up; pathological case
		}
		out = append(out, s[:cut])
		s = s[cut:]
	}
	if len(s) > 0 {
		out = append(out, s)
	}
	return out
}

func attachmentsCacheDir() (string, error) {
	if x := os.Getenv("XDG_CACHE_HOME"); x != "" {
		return filepath.Join(x, "c3", "telegram", "attachments"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("telegram: resolve home: %w", err)
	}
	return filepath.Join(home, ".cache", "c3", "telegram", "attachments"), nil
}
