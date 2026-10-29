package providerkit

import (
	"cmp"
	"math"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/leapmux/leapmux/internal/worker/agent"
)

// TurnFailureKind classifies a turn's failure for the automatic-retry queue.
type TurnFailureKind int

const (
	// TurnFailureNotRetryable is a turn end with no retryable failure.
	TurnFailureNotRetryable TurnFailureKind = iota
	// TurnFailureTransient is a 5xx, overload, network, or timeout failure.
	TurnFailureTransient
	// TurnFailureRateLimited is a 429 with no reset instant.
	TurnFailureRateLimited
	// TurnFailureUsageLimited is a plan usage limit.
	TurnFailureUsageLimited
	// TurnFailureBillingBlocked lifts only on a billing or plan change.
	TurnFailureBillingBlocked
)

// UsageWindow classifies how long a usage limit lasts when the wire states no
// reset instant.
type UsageWindow int

const (
	// UsageWindowUnstated means the wire gives no usable window wording.
	UsageWindowUnstated UsageWindow = iota
	// UsageWindowWeekOrLess means the stated window is at most one week.
	UsageWindowWeekOrLess
	// UsageWindowLonger means the stated window exceeds one week.
	UsageWindowLonger
)

// TurnFailure is the verdict of one turn end.
type TurnFailure struct {
	// Kind classifies the failure.
	Kind TurnFailureKind
	// ResetAt is the instant the usage limit lifts. It applies only to
	// UsageLimited and is zero when the wire states no instant.
	ResetAt time.Time
	// Window classifies a UsageLimited failure with no ResetAt.
	Window UsageWindow
	// Cause is an operator sentence, e.g. "Z.AI reported usage limit 1308".
	// A NotRetryable verdict's cause is the reason of its cancellation, and an
	// empty one cancels with TurnEndedWithoutRetryableFailure.
	Cause string
}

// maxRetryDelay is the longest stated delay or reset instant that a verdict
// accepts: 31 days.
const maxRetryDelay = 31 * 24 * time.Hour

// ApplyTurnFailure applies one verdict to the automatic-retry queue. The
// payload is defensively copied because the caller's buffer may be reused.
func ApplyTurnFailure(writer agent.AutoRetryWriter, failure TurnFailure, payload []byte) {
	p := append([]byte(nil), payload...)
	switch failure.Kind {
	case TurnFailureNotRetryable:
		writer.CancelServerErrorRetry(cmp.Or(failure.Cause, TurnEndedWithoutRetryableFailure))
	case TurnFailureTransient, TurnFailureRateLimited:
		writer.ScheduleServerErrorRetry(p)
	case TurnFailureUsageLimited:
		if !failure.ResetAt.IsZero() {
			writer.ScheduleUsageLimitRetry(failure.ResetAt, p)
			writer.CancelServerErrorRetry("the turn ended on a usage limit that lifts at a stated time")
		} else if failure.Window == UsageWindowWeekOrLess {
			writer.ScheduleServerErrorRetry(p)
		} else {
			writer.CancelServerErrorRetry("the turn ended on a usage limit that states no reset within a week")
		}
	case TurnFailureBillingBlocked:
		reason := failure.Cause + ", which only a billing or plan change lifts"
		writer.CancelUsageLimitRetry(reason)
		writer.CancelServerErrorRetry(reason)
	}
}

// WithResetHint enriches a RateLimited or a ResetAt-less UsageLimited verdict
// with a stated reset instant. Every other verdict is returned unchanged.
func WithResetHint(failure TurnFailure, resetAt, observedAt time.Time) TurnFailure {
	if failure.Kind != TurnFailureRateLimited && (failure.Kind != TurnFailureUsageLimited || !failure.ResetAt.IsZero()) {
		return failure
	}
	if resetAt.IsZero() || !resetAt.After(observedAt) || resetAt.After(observedAt.Add(maxRetryDelay)) {
		return failure
	}
	failure.Kind = TurnFailureUsageLimited
	failure.ResetAt = resetAt
	if failure.Cause != "" {
		failure.Cause += " (the provider states when to retry)"
	} else {
		failure.Cause = "the provider states when to retry"
	}
	return failure
}

// CompactionEnd returns the verdict of a turn that a compaction ends, whatever
// the compaction's outcome: NotRetryable, which cancels a pending server-error
// retry and keeps a pending usage-limit retry. A compaction never schedules a
// retry. A failed compaction leaves the task where the last turn left it, and
// the retry's "Please continue working" would resume a task that the reader
// did not ask to resume. A compaction inside a conversation turn ends no turn,
// so the conversation turn's own verdict applies.
func CompactionEnd() TurnFailure {
	return TurnFailure{Kind: TurnFailureNotRetryable, Cause: TurnEndedOnCompaction}
}

// LongRetryWaitThreshold is the longest retry wait of an agent's own that a
// LeapMux turn sits out. An agent that waits out a usage limit inside the open
// turn can wait for hours, and a restart loses that wait. A longer wait
// therefore ends the turn, and the queue retries at the instant that the agent
// states.
const LongRetryWaitThreshold = 10 * time.Minute

// LongRetryWaitFailure returns the verdict of a turn that LeapMux ends because
// the agent states its next retry at resetAt: a usage limit that lifts at
// resetAt. ok is false for a wait of LongRetryWaitThreshold or less, which the
// agent keeps, and for an instant that the queue cannot schedule.
func LongRetryWaitFailure(resetAt, observedAt time.Time) (failure TurnFailure, ok bool) {
	if resetAt.Sub(observedAt) <= LongRetryWaitThreshold {
		return TurnFailure{}, false
	}
	failure = WithResetHint(TurnFailure{
		Kind:  TurnFailureRateLimited,
		Cause: "the agent stated its next retry more than 10 minutes ahead",
	}, resetAt, observedAt)
	if failure.ResetAt.IsZero() {
		return TurnFailure{}, false
	}
	return failure, true
}

// --- UsageWindowOf ---

var (
	monthWordRe     = regexp.MustCompile(`(?i)month(ly)?|billing\s+cycle`)
	oversizedUnitRe = regexp.MustCompile(`(?i)(?:^|[^\w.+-])(\d{7,})\s?-?\s?(hours?|hrs?|h|days?|d|weeks?|wks?|w)\b`)
	durationTokenRe = regexp.MustCompile(`(?i)(?:^|[^\w.+-])(\d{1,6})\s?-?\s?(hours?|hrs?|h|days?|d|weeks?|wks?|w)\b`)
	wordFormRe      = regexp.MustCompile(`(?i)\b(hourly|daily|weekly)\b`)
)

// unitHours maps a captured unit word to its length in hours.
func unitHours(unit string) int64 {
	switch strings.ToLower(unit) {
	case "h", "hr", "hrs", "hour", "hours":
		return 1
	case "d", "day", "days":
		return 24
	case "w", "wk", "wks", "week", "weeks":
		return 168
	}
	return 0
}

// UsageWindowOf turns window wording into a UsageWindow.
func UsageWindowOf(text string) UsageWindow {
	if text == "" {
		return UsageWindowUnstated
	}
	// Month words win over every other signal.
	if monthWordRe.MatchString(text) {
		return UsageWindowLonger
	}
	// A run of 7 or more digits before a unit is oversized: Longer.
	if oversizedUnitRe.MatchString(text) {
		return UsageWindowLonger
	}
	// Sum the duration tokens in hours.
	matches := durationTokenRe.FindAllStringSubmatch(text, -1)
	if len(matches) > 0 {
		var sum int64
		for _, m := range matches {
			n, err := strconv.ParseInt(m[1], 10, 64)
			if err != nil {
				continue
			}
			if n == 0 {
				continue
			}
			h := unitHours(m[2])
			if h == 0 {
				continue
			}
			product := n * h
			// Checked arithmetic: saturate on overflow.
			if n > 0 && h > 0 && product/h != n {
				return UsageWindowLonger
			}
			if sum > math.MaxInt64-product {
				return UsageWindowLonger
			}
			sum += product
		}
		if sum > 168 {
			return UsageWindowLonger
		}
		if sum > 0 {
			return UsageWindowWeekOrLess
		}
		// sum == 0: all tokens were zero. Fall through to words.
	}
	// Words apply only when no numeric token is present.
	if wordFormRe.MatchString(text) {
		return UsageWindowWeekOrLess
	}
	return UsageWindowUnstated
}

// --- ParseRetryDelay ---

var (
	// retryDelayRe matches a whole delay: an optional `~`, then one or more
	// number-and-unit tokens. Twelve digits admit every millisecond count up
	// to the 31-day maximum, which has ten; the total check sets the limit.
	retryDelayRe      = regexp.MustCompile(`(?i)^\s*~?\s*(?:\d{1,12}(?:\.\d{1,9})?\s*(?:ms|sec|min|s|m|h|d)\s*)+$`)
	retryDelayTokenRe = regexp.MustCompile(`(?i)(\d{1,12}(?:\.\d{1,9})?)\s*(ms|sec|min|s|m|h|d)`)
)

// retryDelayUnit maps a unit token to its canonical name and its length.
func retryDelayUnit(unit string) (string, time.Duration) {
	switch strings.ToLower(unit) {
	case "ms":
		return "ms", time.Millisecond
	case "s", "sec":
		return "s", time.Second
	case "m", "min":
		return "m", time.Minute
	case "h":
		return "h", time.Hour
	case "d":
		return "d", 24 * time.Hour
	}
	return "", 0
}

// ParseRetryDelay reads a whole relative delay such as "5h", "23h 59m",
// "~42 min", "19h14m47s", "34.07s", or "1500ms". The text must be the delay
// alone: a caller extracts it from the sentence first. Each unit appears at
// most once. The delay must be at most 31 days. It returns false for any other
// text, a sign included.
func ParseRetryDelay(text string) (time.Duration, bool) {
	if !retryDelayRe.MatchString(text) {
		return 0, false
	}
	seen := make(map[string]bool)
	var total float64
	for _, token := range retryDelayTokenRe.FindAllStringSubmatch(text, -1) {
		unit, length := retryDelayUnit(token[2])
		if length == 0 || seen[unit] {
			return 0, false
		}
		seen[unit] = true
		value, err := strconv.ParseFloat(token[1], 64)
		if err != nil || value < 0 {
			return 0, false
		}
		total += value * float64(length)
		if total > float64(maxRetryDelay) {
			return 0, false
		}
	}
	return time.Duration(total), true
}

// --- RetryAfterInstant ---

// RetryAfterInstant reads the delay headers of an HTTP response. It matches
// header names case-insensitively. retry-after-ms gives milliseconds;
// retry-after gives seconds or an HTTP date. The base time is the date header
// when it parses, observedAt otherwise. The delay must be more than 0 and at
// most 31 days.
func RetryAfterInstant(headers map[string]string, observedAt time.Time) (time.Time, bool) {
	lookup := func(name string) (string, bool) {
		for k, v := range headers {
			if strings.EqualFold(k, name) {
				return v, true
			}
		}
		return "", false
	}

	// The base time comes from the date header when it parses.
	base := observedAt
	if dateStr, ok := lookup("date"); ok {
		if t, err := http.ParseTime(dateStr); err == nil {
			base = t
		}
	}

	// retry-after-ms wins when both headers are present. A value that does not
	// parse falls back to retry-after, as the OpenAI SDK does.
	if v, ok := lookup("retry-after-ms"); ok {
		if ms, err := strconv.ParseFloat(strings.TrimSpace(v), 64); err == nil {
			d := time.Duration(ms * float64(time.Millisecond))
			if ms <= 0 || d <= 0 || d > maxRetryDelay {
				return time.Time{}, false
			}
			return base.Add(d), true
		}
	}

	if v, ok := lookup("retry-after"); ok {
		v = strings.TrimSpace(v)
		// Try seconds first.
		if secs, err := strconv.ParseFloat(v, 64); err == nil {
			if secs <= 0 {
				return time.Time{}, false
			}
			d := time.Duration(secs * float64(time.Second))
			if d <= 0 || d > maxRetryDelay {
				return time.Time{}, false
			}
			return base.Add(d), true
		}
		// Try HTTP date.
		if t, err := http.ParseTime(v); err == nil {
			d := t.Sub(base)
			if d <= 0 || d > maxRetryDelay {
				return time.Time{}, false
			}
			return t, true
		}
		return time.Time{}, false
	}

	return time.Time{}, false
}
