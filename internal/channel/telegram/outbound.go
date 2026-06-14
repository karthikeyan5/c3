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

// SendReply sends ONE Telegram part and returns its message_id. A part carries
// AT MOST ONE of {text, a single media item, a poll} — the pure capability.Gate
// splits a logical reply into such parts and dispatch sends one part per call.
// Honors message_thread_id when args.TopicID is non-nil and reply_parameters
// when args.ReplyTo is non-nil.
//
// Part-by-content dispatch (P3):
//   - args.Poll != nil → send a poll (sendPoll).
//   - len(args.Media) == 1 → send that one media item by Kind (sendMedia).
//   - else → send args.Text as a single text message (the path below).
//
// Chunking is NOT done here. The gate splits a long logical reply into parts
// that each fit Telegram's limit — SendReply sends one. This removes, by
// construction, the prior silent-success-on-chunk-k>0 bug where a failed Nth
// chunk logged, broke the loop, and returned success.
//
// Markup mapping (channel-neutral intent → Telegram wire):
//   - MarkupMarkdown OR "" (empty/zero value = the MARKDOWN DEFAULT): run
//     mdToTelegramHTML, send parse_mode=HTML. Broker-internal callers (welcome,
//     fallback, ping) construct ReplyArgs WITHOUT setting Markup and rely on the
//     empty value meaning auto-convert; without this their markdown would render
//     as literal characters.
//   - MarkupNative: send the text as-is (pre-formed HTML), parse_mode=HTML.
//   - MarkupNone: plain text, no parse_mode.
func (c *Channel) SendReply(args c3types.ReplyArgs) (int64, error) {
	if c.bot == nil {
		return 0, errors.New("telegram: channel not started")
	}
	// A part carries at most one of {poll, single media item, text}.
	if args.Poll != nil {
		return c.sendPoll(args)
	}
	if len(args.Media) == 1 {
		return c.sendMedia(args, args.Media[0])
	}
	if len(args.Media) > 1 {
		// The gate emits one media item per part; >1 here means a caller bypassed
		// the gate. Fail loudly rather than silently send only the first.
		return 0, fmt.Errorf("telegram: SendReply got %d media items in one part — the gate emits one item per part", len(args.Media))
	}

	// Empty/zero-value Markup is the MARKDOWN DEFAULT (see doc comment).
	convertMd := args.Markup == c3types.MarkupMarkdown || args.Markup == ""
	useHTML := convertMd || args.Markup == c3types.MarkupNative

	text := args.Text
	opts := &gotgbot.SendMessageOpts{}
	if args.TopicID != nil {
		opts.MessageThreadId = *args.TopicID
	}
	if convertMd {
		text = mdToTelegramHTML(text)
	}
	if useHTML {
		opts.ParseMode = "HTML"
	}
	if args.ReplyTo != nil {
		opts.ReplyParameters = &gotgbot.ReplyParameters{
			MessageId:                *args.ReplyTo,
			AllowSendingWithoutReply: true,
		}
	}
	opts.RequestOpts = requestOptsFor("sendMessage", longPollTimeoutSeconds)
	if err := c.rate.Wait(c.ctx, args.ChatID); err != nil {
		return 0, fmt.Errorf("telegram: rate-wait: %w", err)
	}
	msg, err := c.bot.SendMessage(args.ChatID, text, opts)
	if err != nil && convertMd && isParseEntityError(err) {
		// Plaintext fallback (per OpenClaw bot/delivery.send.ts pattern). Our
		// markdown converter occasionally produces malformed HTML for
		// pathological input; re-send the ORIGINAL text as plain text rather
		// than dropping the message.
		c.host.Logf("telegram: HTML parse error, retrying as plaintext: %v", err)
		plainOpts := *opts
		plainOpts.ParseMode = ""
		msg, err = c.bot.SendMessage(args.ChatID, args.Text, &plainOpts)
	}
	if err != nil {
		c.recordOutboundErr(err)
		return 0, fmt.Errorf("telegram: SendMessage: %w", err)
	}
	c.recordOutboundSuccess()
	return msg.MessageId, nil
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
//
// Markup mapping (P2b — the converter is now wired into edits, so an edited
// message renders rich just like a reply; today EditMessage did not convert at
// all). Same rule as SendReply:
//   - MarkupMarkdown OR "" (empty/zero value = the MARKDOWN DEFAULT): run
//     mdToTelegramHTML, send parse_mode=HTML. Broker-internal callers that build
//     EditArgs WITHOUT setting Markup rely on the empty value meaning
//     auto-convert; without this their markdown would render as literal chars.
//   - MarkupNative: send the text as-is (pre-formed HTML), parse_mode=HTML.
//   - MarkupNone: plain text, no parse_mode.
//
// Plaintext fallback on a parse error mirrors SendReply: a malformed-HTML edit
// is retried as the original plain text rather than dropped.
func (c *Channel) EditMessage(args c3types.EditArgs) (*c3types.EditResult, error) {
	if c.bot == nil {
		return nil, errors.New("telegram: channel not started")
	}

	convertMd := args.Markup == c3types.MarkupMarkdown || args.Markup == ""
	useHTML := convertMd || args.Markup == c3types.MarkupNative

	text := args.Text
	opts := &gotgbot.EditMessageTextOpts{
		ChatId:      args.ChatID,
		MessageId:   args.MessageID,
		RequestOpts: requestOptsFor("editMessageText", longPollTimeoutSeconds),
	}
	if convertMd {
		text = mdToTelegramHTML(text)
	}
	if useHTML {
		opts.ParseMode = "HTML"
	}
	if err := c.rate.Wait(c.ctx, args.ChatID); err != nil {
		return nil, fmt.Errorf("telegram: rate-wait: %w", err)
	}
	_, _, err := c.bot.EditMessageText(text, opts)
	if err != nil && convertMd && isParseEntityError(err) {
		c.host.Logf("telegram: HTML parse error on edit, retrying as plaintext: %v", err)
		plainOpts := *opts
		plainOpts.ParseMode = ""
		_, _, err = c.bot.EditMessageText(args.Text, &plainOpts)
	}
	if err != nil {
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

	// Size pre-check (cap-aware): the Bot API download ceiling is 20 MiB. The
	// inbound Attachment.Size is not reachable through the channel.Channel
	// DownloadAttachment(fileID) signature, so we pre-check the GetFile result's
	// FileSize (set for most file types) and reject BEFORE streaming the body,
	// with a clear MB-vs-limit message rather than a late copy failure. FileSize
	// can be 0/absent for some kinds; in that case we fall through and rely on the
	// HTTP layer (a 20 MiB+ file won't have produced a FilePath above anyway).
	if f.FileSize > maxDownloadBytes {
		return "", fmt.Errorf("telegram: attachment too large to download (%d MB > %d MB limit)",
			f.FileSize/(1024*1024), int64(maxDownloadBytes)/(1024*1024))
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
