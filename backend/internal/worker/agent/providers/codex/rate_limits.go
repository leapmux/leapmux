package codex

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"regexp"
	"strconv"
	"time"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
)

// codexDefaultRateLimitID is the bucket of a snapshot whose `limitId` is empty
// or null. "codex" is the ID of Codex's own metered limit, and a snapshot
// without an ID describes that limit.
const codexDefaultRateLimitID = "codex"

// codexRateLimitSnapshot is the `rateLimits` of one account/rateLimits/updated
// notification, as Codex's app-server schema spells it.
type codexRateLimitSnapshot struct {
	LimitID              *string                `json:"limitId"`
	Primary              *codexRateLimitTier    `json:"primary"`
	Secondary            *codexRateLimitTier    `json:"secondary"`
	Credits              *codexRateLimitCredits `json:"credits"`
	RateLimitReachedType *string                `json:"rateLimitReachedType"`
}

// bucketID returns the ID of the bucket that the snapshot updates.
func (snapshot codexRateLimitSnapshot) bucketID() string {
	if snapshot.LimitID == nil || *snapshot.LimitID == "" {
		return codexDefaultRateLimitID
	}
	return *snapshot.LimitID
}

// carriesWindow reports whether the snapshot states a window. Codex sends a
// snapshot with both windows null for a bucket that meters no window, such as
// `premium` beside a used-up `codex` limit.
func (snapshot codexRateLimitSnapshot) carriesWindow() bool {
	return snapshot.Primary != nil || snapshot.Secondary != nil
}

// codexRateLimitTier is one rate-limit window. A WindowDurationMins of 0 means
// that the window states no duration: Codex writes null there, and a window of
// zero minutes does not exist.
type codexRateLimitTier struct {
	UsedPercent        float64 `json:"usedPercent"`
	WindowDurationMins int     `json:"windowDurationMins"`
	ResetsAt           *int64  `json:"resetsAt"`
}

// codexRateLimitCredits is the credit state of one bucket. Codex requires both
// fields whenever it sends the object.
type codexRateLimitCredits struct {
	HasCredits bool `json:"hasCredits"`
	Unlimited  bool `json:"unlimited"`
}

// codexRateLimitBucket is the merged state of one Codex limit. Each
// account/rateLimits/updated is a sparse update: Codex's own schema states
// that a null value does not clear a value that an earlier update gave. One
// snapshot alone therefore cannot say that a window recovered.
type codexRateLimitBucket struct {
	primary   *codexRateLimitTier
	secondary *codexRateLimitTier
	credits   *codexRateLimitCredits
}

// merge applies one snapshot to the bucket. A null window or a null credit
// object keeps the merged one. The merged values are never written in place,
// so a value that a reader took stays unchanged.
func (bucket *codexRateLimitBucket) merge(snapshot codexRateLimitSnapshot) {
	bucket.primary = mergeCodexRateLimitTier(bucket.primary, snapshot.Primary)
	bucket.secondary = mergeCodexRateLimitTier(bucket.secondary, snapshot.Secondary)
	if snapshot.Credits != nil {
		credits := *snapshot.Credits
		bucket.credits = &credits
	}
}

// windows returns the merged windows that the bucket holds, primary first.
func (bucket *codexRateLimitBucket) windows() []*codexRateLimitTier {
	windows := make([]*codexRateLimitTier, 0, 2)
	for _, window := range []*codexRateLimitTier{bucket.primary, bucket.secondary} {
		if window != nil {
			windows = append(windows, window)
		}
	}
	return windows
}

// creditsBlock reports whether a used-up window of the bucket blocks requests.
// Credits keep serving requests after a window is used up, so only credits that
// state both no balance and no unlimited plan prove the block. A bucket whose
// credits no snapshot stated does not block.
func (bucket *codexRateLimitBucket) creditsBlock() bool {
	return bucket.credits != nil && !bucket.credits.HasCredits && !bucket.credits.Unlimited
}

// holdsUsedUpWindow reports whether a merged window of the bucket is at or
// above its limit.
func (bucket *codexRateLimitBucket) holdsUsedUpWindow() bool {
	for _, window := range bucket.windows() {
		if codexWindowUsedUp(window) {
			return true
		}
	}
	return false
}

// codexWindowUsedUp reports whether a window is at or above its limit.
func codexWindowUsedUp(window *codexRateLimitTier) bool {
	return providerkit.RateLimitStatusForUtilization(window.UsedPercent/100) == contracts.RateLimitStatusExceeded
}

// mergeCodexRateLimitTier merges one window slot. A null window keeps the
// merged one. A window of another stated duration replaces it whole, because
// the merged reset belongs to the other window. Otherwise a null field keeps
// the merged value.
func mergeCodexRateLimitTier(merged, update *codexRateLimitTier) *codexRateLimitTier {
	if update == nil {
		return merged
	}
	next := *update
	if merged == nil {
		return &next
	}
	if next.WindowDurationMins != 0 && merged.WindowDurationMins != 0 && next.WindowDurationMins != merged.WindowDurationMins {
		return &next
	}
	if next.WindowDurationMins == 0 {
		next.WindowDurationMins = merged.WindowDurationMins
	}
	if next.ResetsAt == nil {
		next.ResetsAt = merged.ResetsAt
	}
	return &next
}

// mergeRateLimitSnapshotLocked merges the snapshot into its bucket and returns
// the bucket. The caller holds a.Mu.
func (a *Agent) mergeRateLimitSnapshotLocked(snapshot codexRateLimitSnapshot) *codexRateLimitBucket {
	id := snapshot.bucketID()
	bucket := a.rateLimitBuckets[id]
	if bucket == nil {
		if a.rateLimitBuckets == nil {
			a.rateLimitBuckets = make(map[string]*codexRateLimitBucket)
		}
		bucket = &codexRateLimitBucket{}
		a.rateLimitBuckets[id] = bucket
		a.rateLimitBucketOrder = append(a.rateLimitBucketOrder, id)
	}
	bucket.merge(snapshot)
	return bucket
}

// displayedRateLimitBucketLocked returns the bucket that the session surfaces
// show: the `codex` bucket when it holds a window, or else the first bucket
// seen that holds one. It returns an empty ID and nil when no bucket holds a
// window. The caller holds a.Mu.
func (a *Agent) displayedRateLimitBucketLocked() (string, *codexRateLimitBucket) {
	if bucket := a.rateLimitBuckets[codexDefaultRateLimitID]; bucket != nil && len(bucket.windows()) > 0 {
		return codexDefaultRateLimitID, bucket
	}
	for _, id := range a.rateLimitBucketOrder {
		if bucket := a.rateLimitBuckets[id]; len(bucket.windows()) > 0 {
			return id, bucket
		}
	}
	return "", nil
}

// rateLimitBucketBlocksLocked reports whether a bucket other than exceptID
// can still block: it holds a used-up window, whatever its credits state.
// The clear of one bucket then keeps the pending retry. A used-up window with
// credits can still block: in the real usage-limit sequence the `codex` bucket
// reads 100% with credits, and the turn then fails on that very limit. The
// caller holds a.Mu.
func (a *Agent) rateLimitBucketBlocksLocked(exceptID string) bool {
	for id, bucket := range a.rateLimitBuckets {
		if id != exceptID && bucket.holdsUsedUpWindow() {
			return true
		}
	}
	return false
}

// codexUsageLimitResetLocked returns the latest reset of a used-up window of
// the merged `codex` bucket that can schedule a retry at now, or nil. The
// caller holds a.Mu.
func (a *Agent) codexUsageLimitResetLocked(now time.Time) *time.Time {
	bucket := a.rateLimitBuckets[codexDefaultRateLimitID]
	if bucket == nil {
		return nil
	}
	return readCodexRateLimitBucket(bucket, now).latestExceededReset
}

// handleRateLimitsUpdated merges one sparse account snapshot into its bucket,
// publishes the displayed bucket to the live session surfaces, and schedules
// or cancels the usage-limit retry. Account snapshots never become transcript
// rows.
func (a *Agent) handleRateLimitsUpdated(content []byte, params json.RawMessage) {
	var notif struct {
		RateLimits codexRateLimitSnapshot `json:"rateLimits"`
	}
	if err := json.Unmarshal(params, &notif); err != nil {
		slog.Warn("codex rate limit unmarshal failed", "agent_id", a.AgentID(), "error", err)
		return
	}
	snapshot := notif.RateLimits
	// The reached type is read from each snapshot alone. No value of it states
	// that a block ended, so a merged type would never clear.
	reachedType := ""
	if snapshot.RateLimitReachedType != nil {
		reachedType = *snapshot.RateLimitReachedType
	}
	now := a.Clock().Now()

	update := codexRateLimitUpdate{
		bucketID:      snapshot.bucketID(),
		reachedType:   reachedType,
		carriedWindow: snapshot.carriesWindow(),
	}
	a.Mu.Lock()
	update.bucket = readCodexRateLimitBucket(a.mergeRateLimitSnapshotLocked(snapshot), now)
	update.otherBucketBlocks = a.rateLimitBucketBlocksLocked(update.bucketID)
	displayedID, displayed := a.displayedRateLimitBucketLocked()
	var displayedWindows []*codexRateLimitTier
	if displayed != nil {
		displayedWindows = displayed.windows()
	}
	a.Mu.Unlock()

	// The reached type confirms the most-used window only for its own bucket.
	confirmed := reachedType == codexRateLimitReachedTimeWindow && displayedID == update.bucketID
	a.sink.BroadcastSessionInfo(map[string]interface{}{
		contracts.SessionInfoKeyRateLimits: map[string]interface{}{
			contracts.RateLimitUpdateFieldMode:   contracts.RateLimitUpdateModeReplace,
			contracts.RateLimitUpdateFieldValues: codexRateLimitDisplay(displayedWindows, reachedType, confirmed),
		},
	})

	retry := codexUsageLimitRetryFor(update)
	switch {
	case retry.resumeAt != nil:
		a.sink.ScheduleUsageLimitRetry(*retry.resumeAt, append([]byte(nil), content...))
	case retry.cancelReason != "":
		a.sink.CancelUsageLimitRetry(retry.cancelReason)
	}
}

// codexRateLimitDisplay builds the session-info values of the displayed
// windows. confirmed states that the snapshot's reached type confirms a timed
// block of these windows, so the most-used window shows as exceeded even when
// Codex rounded its percentage below 100.
//
// The values always hold the account-block member: an empty member clears a
// billing or workspace block that an earlier snapshot showed.
func codexRateLimitDisplay(windows []*codexRateLimitTier, reachedType string, confirmed bool) map[string]interface{} {
	values := map[string]interface{}{}
	var bindingKey string
	bindingPct := -1.0
	anyExceeded := false
	for slot, window := range windows {
		rateLimitType := codexWindowToType(window.WindowDurationMins)
		if rateLimitType == "" {
			// A window that states no duration keeps a key of its own.
			rateLimitType = fmt.Sprintf("window_%d", slot+1)
		}
		utilization := window.UsedPercent / 100
		status := providerkit.RateLimitStatusForUtilization(utilization)
		if status == contracts.RateLimitStatusExceeded {
			anyExceeded = true
		}
		info := map[string]interface{}{
			contracts.RateLimitFieldRateLimitType: rateLimitType,
			contracts.RateLimitFieldUtilization:   utilization,
			contracts.RateLimitFieldStatus:        status,
		}
		if window.ResetsAt != nil {
			info[contracts.RateLimitFieldResetsAt] = *window.ResetsAt
		}
		if window.UsedPercent > bindingPct {
			bindingPct = window.UsedPercent
			bindingKey = rateLimitType
		}
		values[rateLimitType] = info
	}
	if confirmed && !anyExceeded && bindingKey != "" {
		if info, ok := values[bindingKey].(map[string]interface{}); ok {
			info[contracts.RateLimitFieldStatus] = contracts.RateLimitStatusExceeded
		}
	}
	accountBlock := map[string]interface{}{}
	if reachedType != "" && reachedType != codexRateLimitReachedTimeWindow {
		accountBlock[contracts.RateLimitFieldRateLimitType] = reachedType
		accountBlock[contracts.RateLimitFieldStatus] = contracts.RateLimitStatusExceeded
	}
	values[codexRateLimitAccountBlockKey] = accountBlock
	return values
}

// codexRateLimitReading is what the usage-limit decisions read from one merged
// bucket at the instant of the reading. Each reset field holds only an instant
// that can schedule a retry then: a reset at or before that instant, or more
// than 31 days after it, reads as absent.
type codexRateLimitReading struct {
	// latestExceededReset is the latest reset of a used-up window.
	latestExceededReset *time.Time
	// bindingReset is the reset of the most-used window.
	bindingReset *time.Time
	// latestReset is the latest reset of any window.
	latestReset *time.Time
	// anyExceeded reports a window at or above its limit, with or without a
	// reset.
	anyExceeded bool
	// creditsBlock reports that the bucket's credits do not serve a request
	// that a used-up window refuses.
	creditsBlock bool
}

// readCodexRateLimitBucket reads the merged windows of one bucket at now.
func readCodexRateLimitBucket(bucket *codexRateLimitBucket, now time.Time) codexRateLimitReading {
	reading := codexRateLimitReading{creditsBlock: bucket.creditsBlock()}
	bindingPct := -1.0
	for _, window := range bucket.windows() {
		exceeded := codexWindowUsedUp(window)
		if exceeded {
			reading.anyExceeded = true
		}
		var reset *time.Time
		if window.ResetsAt != nil {
			resetAt := time.Unix(*window.ResetsAt, 0).UTC()
			if codexUsableReset(resetAt, now) {
				reset = &resetAt
			}
		}
		if reset != nil {
			reading.latestReset = laterCodexReset(reading.latestReset, reset)
			if exceeded {
				reading.latestExceededReset = laterCodexReset(reading.latestExceededReset, reset)
			}
		}
		if window.UsedPercent > bindingPct {
			bindingPct = window.UsedPercent
			reading.bindingReset = reset
		}
	}
	return reading
}

// laterCodexReset returns the later of two resets, either of which can be nil.
func laterCodexReset(current, candidate *time.Time) *time.Time {
	if current == nil || candidate.After(*current) {
		return candidate
	}
	return current
}

// codexUsableReset reports whether a stated reset can schedule a usage-limit
// retry observed at now. It applies the bound of providerkit.WithResetHint, so
// a snapshot and a turn failure accept the same instants. A past reset would
// schedule a retry that fires at once and fails again.
func codexUsableReset(resetAt, now time.Time) bool {
	hinted := providerkit.WithResetHint(providerkit.TurnFailure{Kind: providerkit.TurnFailureRateLimited}, resetAt, now)
	return !hinted.ResetAt.IsZero()
}

// codexRateLimitReachedTimeWindow identifies the Codex limit that the rolling-window timer clears.
// The other limits require a billing or usage change.
const codexRateLimitReachedTimeWindow = "rate_limit_reached"

// codexRateLimitAccountBlockKey identifies a billing or workspace block independently from the rolling windows.
const codexRateLimitAccountBlockKey = "account_block"

// codexUsageLimitRetry is what one rate-limit snapshot does to the pending
// usage-limit retry. A resumeAt schedules the retry at that instant, and a
// cancelReason cancels it. With neither, the retry stays as it is: a block
// without a usable reset supplies no deadline, so the retry keeps the one that
// an earlier snapshot or turn gave. Claude keeps a pending resume for an
// undated block by the same rule.
type codexUsageLimitRetry struct {
	resumeAt     *time.Time
	cancelReason string
}

// codexRateLimitUpdate is what one snapshot states, with the merged state that
// the usage-limit decision reads.
type codexRateLimitUpdate struct {
	// bucketID identifies the bucket that the snapshot updates.
	bucketID string
	// reachedType is the snapshot's own rateLimitReachedType, or "".
	reachedType string
	// carriedWindow states that the snapshot carried a window for the bucket.
	carriedWindow bool
	// bucket is the reading of the updated bucket after the merge.
	bucket codexRateLimitReading
	// otherBucketBlocks reports that another bucket holds a used-up window that
	// its credits do not serve.
	otherBucketBlocks bool
}

// codexUsageLimitRetryFor decides what one snapshot does to the pending
// usage-limit retry.
//
//   - `rate_limit_reached` resumes at the latest reset of a used-up window, or
//     else at the reset of the most-used window, or else at the latest reset.
//   - Another reached type is a billing or workspace block, which cancels.
//   - With no reached type, the snapshot resumes only at the reset of a window
//     that blocks: a window at 100% blocks only when the bucket's credits do
//     not serve the request. It cancels only when it carried a window, every
//     merged window of its bucket is below 100%, and no other bucket blocks:
//     the clear of one limit does not lift the block of another.
func codexUsageLimitRetryFor(update codexRateLimitUpdate) codexUsageLimitRetry {
	reading := update.bucket
	switch update.reachedType {
	case "":
		if reading.creditsBlock && reading.latestExceededReset != nil {
			return codexUsageLimitRetry{resumeAt: reading.latestExceededReset}
		}
		if update.carriedWindow && !reading.anyExceeded && !update.otherBucketBlocks {
			return codexUsageLimitRetry{cancelReason: fmt.Sprintf(
				"Codex reported that no usage window of the %q limit is exceeded", update.bucketID)}
		}
		return codexUsageLimitRetry{}
	case codexRateLimitReachedTimeWindow:
		for _, reset := range []*time.Time{reading.latestExceededReset, reading.bindingReset, reading.latestReset} {
			if reset != nil {
				return codexUsageLimitRetry{resumeAt: reset}
			}
		}
		return codexUsageLimitRetry{}
	default:
		return codexUsageLimitRetry{cancelReason: fmt.Sprintf(
			"Codex reported the block %q, which only a billing or usage change lifts", update.reachedType)}
	}
}

// codexWindowToType returns the session-info type of a window, or "" for a
// window that states no duration.
func codexWindowToType(mins int) string {
	switch {
	case mins <= 0:
		return ""
	case mins == 300:
		return contracts.RateLimitTypeFiveHour
	case mins == 10080:
		return contracts.RateLimitTypeSevenDay
	case mins >= 1440:
		return fmt.Sprintf("%d_day", (mins+720)/1440)
	default:
		return fmt.Sprintf("%d_hour", (mins+30)/60)
	}
}

// codexRetryTextPattern matches the retry time in Codex's usage-limit message:
// "try again at Nov 2nd, 2026 3:00 PM", or "try again at 3:00 PM" on the same
// local day. codex-rs format_retry_timestamp writes the time in the local zone
// of the Codex process and truncates it to the minute.
var codexRetryTextPattern = regexp.MustCompile(`\b[Tt]ry again at (?:([A-Z][a-z]{2}) ([1-9]|[12][0-9]|3[01])(?:st|nd|rd|th), ([0-9]{4}) )?(1[0-2]|[1-9]):([0-5][0-9]) ([AP]M)\b`)

// codexRetryTextSlack is how long past the stated minute the retry waits. The
// text drops the seconds of the real reset, which lies inside that minute.
const codexRetryTextSlack = 60 * time.Second

// parseCodexRetryText reads the retry time of a usage-limit message in
// location, the local zone of the Codex process, and returns it in UTC. A
// message without a date states a time of the local day of now. It returns
// false for a message that states no valid time.
//
// The turn frame states no time of its own, so the day comes from now. A
// failure that Codex states just before local midnight and that LeapMux reads
// just after it reads the time on the new day, about a day late. The window is
// the seconds between the failure and its handling, and the reset of the
// merged `codex` bucket, when a snapshot stated one, decides before this text.
func parseCodexRetryText(message string, location *time.Location, now time.Time) (time.Time, bool) {
	match := codexRetryTextPattern.FindStringSubmatch(message)
	if match == nil {
		return time.Time{}, false
	}
	if location == nil {
		location = time.Local
	}
	year, month, day := now.In(location).Date()
	if match[1] != "" {
		parsedMonth, err := time.Parse("Jan", match[1])
		if err != nil {
			return time.Time{}, false
		}
		month = parsedMonth.Month()
		day, _ = strconv.Atoi(match[2])
		year, _ = strconv.Atoi(match[3])
	}
	hour, _ := strconv.Atoi(match[4])
	minute, _ := strconv.Atoi(match[5])
	hour %= 12
	if match[6] == "PM" {
		hour += 12
	}
	stated := time.Date(year, month, day, hour, minute, 0, 0, location)
	// time.Date normalizes a day that the month lacks, such as Feb 30th.
	if statedYear, statedMonth, statedDay := stated.Date(); statedYear != year || statedMonth != month || statedDay != day {
		return time.Time{}, false
	}
	return stated.Add(codexRetryTextSlack).UTC(), true
}

// codexUsageLimitSources holds where a turn that failed with
// usageLimitExceeded finds the instant that its limit lifts.
type codexUsageLimitSources struct {
	// bucketReset is the latest reset of a used-up window of the merged `codex`
	// bucket that lies after now, or nil.
	bucketReset *time.Time
	// textLocation is the zone of the local time in the message. Nil reads as
	// time.Local.
	textLocation *time.Location
}

// codexUsageLimitFailure is the verdict of a turn that failed with
// usageLimitExceeded. The error itself states no structured reset, so the
// reset comes from the merged `codex` bucket, or else from the time in the
// message. Each instant goes through providerkit.WithResetHint. With neither
// instant, the verdict states no window, so the turn end only cancels the
// server-error retry and keeps a usage-limit retry that a snapshot scheduled.
func codexUsageLimitFailure(message string, sources codexUsageLimitSources, now time.Time) providerkit.TurnFailure {
	failure := providerkit.TurnFailure{Kind: providerkit.TurnFailureUsageLimited, Cause: "Codex reported usageLimitExceeded"}
	if sources.bucketReset != nil {
		if hinted := providerkit.WithResetHint(failure, *sources.bucketReset, now); !hinted.ResetAt.IsZero() {
			return hinted
		}
	}
	if resetAt, ok := parseCodexRetryText(message, sources.textLocation, now); ok {
		return providerkit.WithResetHint(failure, resetAt, now)
	}
	return failure
}
