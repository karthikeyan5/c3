package telegram

import "testing"

// TestAllowedReactionEmoji_Membership pins the Telegram-documented standard
// reaction set: a representative sample of allowed emoji (including the
// zero-width-joiner sequences like 🤷‍♂ and ❤‍🔥 that are easy to mangle) must be
// present, and plainly-disallowed inputs must be absent. This allowlist is what
// turns an opaque Telegram 400 into a clear, actionable error in React.
func TestAllowedReactionEmoji_Membership(t *testing.T) {
	for _, e := range []string{"👍", "👎", "❤", "🔥", "🤷‍♂", "❤‍🔥", "👨‍💻", "😡"} {
		if _, ok := allowedReactionEmoji[e]; !ok {
			t.Errorf("expected %q to be an allowed reaction emoji", e)
		}
	}
	for _, e := range []string{"🍕", "x", "", "👍👍"} {
		if _, ok := allowedReactionEmoji[e]; ok {
			t.Errorf("did not expect %q to be an allowed reaction emoji", e)
		}
	}
	// Pin the documented count so an accidental edit to the set is loud.
	if got, want := len(allowedReactionEmoji), 73; got != want {
		t.Errorf("allowedReactionEmoji has %d entries, want %d (Telegram's documented set)", got, want)
	}
}
