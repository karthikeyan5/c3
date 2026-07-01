package c3types

import "fmt"

// ReplyContextFields renders the reply-context metadata fields shared by the
// queued (fetch_queue) renderer in both adapters and the Codex live-forward turn
// header. Returning a single slice from one place is what keeps those renderers
// from drifting (the queued renderers are required to stay byte-identical, and the
// live-forward header must match their reply formatting). Order is stable:
//
//	reply_to=<id> [reply_to_user=@<name>|reply_to_user=<id>] [reply_to_text=<quoted>]
//
// Returns nil when rc is nil (so callers can append the result unconditionally).
func ReplyContextFields(rc *ReplyContext) []string {
	if rc == nil {
		return nil
	}
	fields := []string{fmt.Sprintf("reply_to=%d", rc.MessageID)}
	if rc.User.Username != "" {
		fields = append(fields, "reply_to_user=@"+rc.User.Username)
	} else if rc.User.UserID != 0 {
		fields = append(fields, fmt.Sprintf("reply_to_user=%d", rc.User.UserID))
	}
	if rc.Text != "" {
		fields = append(fields, fmt.Sprintf("reply_to_text=%q", rc.Text))
	}
	return fields
}

// AttachmentField renders one attachment's full metadata (kind, file_id, mime,
// size, name) in the exact format the queued (fetch_queue) renderers emit, so the
// live-forward header and both adapters' renderers share a single source of truth.
// The file_id/mime are load-bearing: the agent uses them to recover backlog media
// via download_attachment/retranscribe.
func AttachmentField(att Attachment) string {
	return fmt.Sprintf("attachment{kind=%s file_id=%q mime=%s size=%d name=%q}",
		att.Kind, att.FileID, att.MIME, att.Size, att.Name)
}
