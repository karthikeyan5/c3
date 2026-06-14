package telegram

import (
	"errors"
	"fmt"

	"github.com/PaulSonOfLars/gotgbot/v2"

	"github.com/karthikeyan5/c3/internal/c3types"
)

// sendPoll sends a regular (non-quiz) Telegram poll for the given PollSpec and
// returns its message_id. Called from SendReply when a part carries a Poll.
//
// Quiz mode is NOT implemented in v1 (PollSpec has no correct-answer field);
// a regular poll is sufficient. Honors message_thread_id (TopicID) and
// reply_parameters (ReplyTo) like the other send paths.
//
// This lives in its own file by design — poll.go is the UNRELATED getUpdates
// poll loop, not the SendPoll path.
func (c *Channel) sendPoll(args c3types.ReplyArgs) (int64, error) {
	if c.bot == nil {
		return 0, errors.New("telegram: channel not started")
	}
	spec := args.Poll
	if spec == nil {
		return 0, errors.New("telegram: sendPoll called with nil poll")
	}
	if spec.Question == "" {
		return 0, errors.New("telegram: poll question is empty")
	}
	if len(spec.Options) < 2 {
		return 0, fmt.Errorf("telegram: a poll needs at least 2 options; got %d", len(spec.Options))
	}

	options := make([]gotgbot.InputPollOption, 0, len(spec.Options))
	for _, o := range spec.Options {
		options = append(options, gotgbot.InputPollOption{Text: o})
	}

	// Telegram defaults IsAnonymous to true; mirror PollSpec.Anonymous explicitly
	// so a public poll is honored. gotgbot takes *bool here.
	anon := spec.Anonymous
	opts := &gotgbot.SendPollOpts{
		IsAnonymous:           &anon,
		AllowsMultipleAnswers: spec.MultipleAnswers,
		MessageThreadId:       threadID(args.TopicID),
		ReplyParameters:       replyParams(args.ReplyTo),
		RequestOpts:           requestOptsFor("sendPoll", longPollTimeoutSeconds),
	}

	if err := c.rate.Wait(c.ctx, args.ChatID); err != nil {
		return 0, fmt.Errorf("telegram: rate-wait: %w", err)
	}
	msg, err := c.bot.SendPoll(args.ChatID, spec.Question, options, opts)
	if err != nil {
		c.recordOutboundErr(err)
		return 0, fmt.Errorf("telegram: SendPoll: %w", err)
	}
	c.recordOutboundSuccess()
	return msg.MessageId, nil
}
