package telegram

import (
	"errors"
	"fmt"

	"github.com/PaulSonOfLars/gotgbot/v2"

	"github.com/karthikeyan5/c3/internal/c3types"
)

// sendPoll sends a Telegram poll for the given PollSpec and returns its
// message_id. Called from SendReply when a part carries a Poll. It builds the
// full sendPoll surface: regular OR quiz (type + correct option + optional
// explanation), plus an optional timer (open_period OR close_date).
//
// Poll-shape validation (option count, quiz/timed rules) is owned by the pure
// capability gate; this path trusts a validated spec and only guards the
// channel-state preconditions. Honors message_thread_id (TopicID) and
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

	// Quiz mode: set the wire type, the correct option (rc.34 uses the SINGULAR
	// CorrectOptionId — NOT the live API's plural correct_option_ids; we follow
	// the pinned dep), and the explanation (rendered through the same markdown→
	// HTML converter as message text, with HTML parse mode). The gate guarantees
	// CorrectOption is non-nil and in range for a quiz.
	if spec.Kind == c3types.PollQuiz {
		opts.Type = "quiz"
		if spec.CorrectOption != nil {
			opts.CorrectOptionId = int64(*spec.CorrectOption)
		}
		if spec.Explanation != "" {
			opts.Explanation = mdToTelegramHTML(spec.Explanation)
			opts.ExplanationParseMode = "HTML"
		}
	}

	// Timed poll: open_period (seconds) OR close_date (Unix ts). The gate
	// enforces mutual exclusivity; 0 means unset.
	if spec.OpenPeriodSec != 0 {
		opts.OpenPeriod = int64(spec.OpenPeriodSec)
	}
	if spec.CloseDateUnix != 0 {
		opts.CloseDate = spec.CloseDateUnix
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
