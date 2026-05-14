package telegram

import (
	"context"
	"errors"
	"net"
	"syscall"
	"testing"
	"time"

	"github.com/PaulSonOfLars/gotgbot/v2"
)

// ─── classifyError ──────────────────────────────────────────────────────────

func TestClassifyError_NilIsNone(t *testing.T) {
	class, ra := classifyError(nil)
	if class != errClassNone || ra != 0 {
		t.Errorf("got (%v, %d), want (none, 0)", class, ra)
	}
}

func TestClassifyError_401Permanent(t *testing.T) {
	err := &gotgbot.TelegramError{Code: 401}
	if class, _ := classifyError(err); class != errClassPermanent {
		t.Errorf("401 → %v, want permanent", class)
	}
}

func TestClassifyError_403Permanent(t *testing.T) {
	err := &gotgbot.TelegramError{Code: 403}
	if class, _ := classifyError(err); class != errClassPermanent {
		t.Errorf("403 → %v, want permanent", class)
	}
}

func TestClassifyError_409Conflict(t *testing.T) {
	err := &gotgbot.TelegramError{Code: 409}
	if class, _ := classifyError(err); class != errClassConflict {
		t.Errorf("409 → %v, want conflict", class)
	}
}

func TestClassifyError_429ExtractsRetryAfter(t *testing.T) {
	err := &gotgbot.TelegramError{
		Code:           429,
		ResponseParams: &gotgbot.ResponseParameters{RetryAfter: 17},
	}
	class, ra := classifyError(err)
	if class != errClassRateLimited {
		t.Errorf("429 → %v, want rate_limited", class)
	}
	if ra != 17 {
		t.Errorf("retry_after = %d, want 17", ra)
	}
}

func TestClassifyError_429WithoutResponseParams(t *testing.T) {
	// Defensive: gotgbot may surface 429 without filling RetryAfter.
	err := &gotgbot.TelegramError{Code: 429}
	class, ra := classifyError(err)
	if class != errClassRateLimited {
		t.Errorf("429 → %v, want rate_limited", class)
	}
	if ra != 0 {
		t.Errorf("retry_after = %d, want 0 (default)", ra)
	}
}

func TestClassifyError_Other4xxPermanent(t *testing.T) {
	// 400 bad request, 404 chat not found, etc. — won't fix on retry.
	for _, code := range []int{400, 404, 418} {
		err := &gotgbot.TelegramError{Code: code}
		if class, _ := classifyError(err); class != errClassPermanent {
			t.Errorf("%d → %v, want permanent", code, class)
		}
	}
}

func TestClassifyError_5xxTransient(t *testing.T) {
	for _, code := range []int{500, 502, 503} {
		err := &gotgbot.TelegramError{Code: code}
		if class, _ := classifyError(err); class != errClassTransient {
			t.Errorf("%d → %v, want transient", code, class)
		}
	}
}

func TestClassifyError_NetworkErrorTransient(t *testing.T) {
	if class, _ := classifyError(context.DeadlineExceeded); class != errClassTransient {
		t.Errorf("context.DeadlineExceeded → %v, want transient", class)
	}
	if class, _ := classifyError(syscall.ECONNRESET); class != errClassTransient {
		t.Errorf("ECONNRESET → %v, want transient", class)
	}
	if class, _ := classifyError(errors.New("EOF")); class != errClassTransient {
		t.Errorf("\"EOF\" → %v, want transient (substring match)", class)
	}
}

func TestErrClass_StringRoundTrip(t *testing.T) {
	cases := map[errClass]string{
		errClassNone:        "none",
		errClassPermanent:   "permanent",
		errClassRateLimited: "rate_limited",
		errClassConflict:    "conflict",
		errClassTransient:   "transient",
	}
	for c, want := range cases {
		if c.String() != want {
			t.Errorf("%d.String() = %q, want %q", c, c.String(), want)
		}
	}
}

// ─── isTransientNetworkError ────────────────────────────────────────────────

type fakeNetTimeoutErr struct{}

func (fakeNetTimeoutErr) Error() string   { return "fake timeout" }
func (fakeNetTimeoutErr) Timeout() bool   { return true }
func (fakeNetTimeoutErr) Temporary() bool { return false }

func TestIsTransientNetworkError_RecognizesContextCancel(t *testing.T) {
	if !isTransientNetworkError(context.Canceled) {
		t.Error("context.Canceled should be transient")
	}
}

func TestIsTransientNetworkError_RecognizesNetErrorTimeout(t *testing.T) {
	var err net.Error = fakeNetTimeoutErr{}
	if !isTransientNetworkError(err) {
		t.Error("net.Error with Timeout()=true should be transient")
	}
}

func TestIsTransientNetworkError_RecognizesByMessageSubstring(t *testing.T) {
	for _, msg := range []string{
		"dial tcp: connection refused",
		"read tcp: connection reset by peer",
		"lookup api.telegram.org: no such host",
		"context deadline exceeded (Client.Timeout: i/o timeout)",
		"http: server closed idle connection",
		"write: broken pipe",
	} {
		if !isTransientNetworkError(errors.New(msg)) {
			t.Errorf("substring match should be transient: %q", msg)
		}
	}
}

func TestIsTransientNetworkError_GenericErrorFalse(t *testing.T) {
	if isTransientNetworkError(errors.New("something else entirely")) {
		t.Error("generic error should not match transient")
	}
}

// ─── timeoutFor ─────────────────────────────────────────────────────────────

func TestTimeoutFor_GetUpdatesIncludesLongPollBudget(t *testing.T) {
	// getUpdates with long_polling=25 → 25s + 30s slack = 55s.
	got := timeoutFor("getUpdates", 25)
	want := 55 * time.Second
	if got != want {
		t.Errorf("getUpdates(25) = %v, want %v", got, want)
	}
}

func TestTimeoutFor_ControlCalls10s(t *testing.T) {
	for _, m := range []string{"getMe", "deleteWebhook", "setMyCommands", "setWebhook", "logOut", "close"} {
		if got := timeoutFor(m, 25); got != 10*time.Second {
			t.Errorf("%s = %v, want 10s", m, got)
		}
	}
}

func TestTimeoutFor_GetFileLong(t *testing.T) {
	if got := timeoutFor("getFile", 25); got != 30*time.Second {
		t.Errorf("getFile = %v, want 30s", got)
	}
}

func TestTimeoutFor_DefaultForSends(t *testing.T) {
	for _, m := range []string{"sendMessage", "editMessageText", "setMessageReaction", "createForumTopic"} {
		if got := timeoutFor(m, 25); got != 20*time.Second {
			t.Errorf("%s = %v, want 20s (default)", m, got)
		}
	}
}

// ─── authBreaker ────────────────────────────────────────────────────────────

func TestAuthBreaker_StartsUntripped(t *testing.T) {
	b := newAuthBreaker(3)
	if b.IsTripped() {
		t.Error("new breaker should be untripped")
	}
	if b.Consec() != 0 {
		t.Errorf("Consec = %d, want 0", b.Consec())
	}
}

func TestAuthBreaker_TripsAtThreshold(t *testing.T) {
	b := newAuthBreaker(3)
	if b.RecordFail() {
		t.Error("1st fail: tripped=true, want false")
	}
	if b.RecordFail() {
		t.Error("2nd fail: tripped=true, want false")
	}
	if !b.RecordFail() {
		t.Error("3rd fail: tripped=false, want true (threshold reached)")
	}
	if !b.IsTripped() {
		t.Error("IsTripped after 3 fails = false, want true")
	}
}

func TestAuthBreaker_SuccessClears(t *testing.T) {
	b := newAuthBreaker(3)
	b.RecordFail()
	b.RecordFail()
	b.RecordSuccess()
	if b.Consec() != 0 {
		t.Errorf("Consec after success = %d, want 0", b.Consec())
	}
	if b.IsTripped() {
		t.Error("breaker should not be tripped after success clears it")
	}
	// And subsequent fails accumulate from zero.
	if b.RecordFail() {
		t.Error("post-clear 1st fail: should not re-trip immediately")
	}
}

func TestAuthBreaker_StaysTrippedUntilSuccess(t *testing.T) {
	b := newAuthBreaker(2)
	b.RecordFail()
	b.RecordFail() // trips
	// More fails should keep it tripped.
	for i := 0; i < 5; i++ {
		if !b.RecordFail() {
			t.Errorf("fail %d: tripped=false, want stays tripped", i+3)
		}
	}
}
