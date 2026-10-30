// Package modelvendor classifies the error text of a model vendor into a
// turn-failure verdict. Each provider extracts the error text from its own
// wire, and this package reads the vendor's own wording. GLM (through Z.AI),
// GPT, MiMo, and Kimi models run behind many agents, so one vendor's wording
// reaches LeapMux through several providers.
//
// A rule needs the vendor's full sentence, its distinctive snake_case code, or
// a numeric code in a code field. A bare number, an HTTP status alone, and
// rate_limit_error alone never decide: a request ID or a token count holds
// numbers that look like Z.AI codes, and rate_limit_error covers several
// unrelated classes.
package modelvendor

import (
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
)

// Verdict is the result of classifying vendor error text.
type Verdict struct {
	// Failure is the turn-failure verdict.
	Failure providerkit.TurnFailure
	// Definitive marks a verdict that a provider must not override: rule A (a
	// usage limit with a reset instant), rule C (a billing block), or a blocked
	// account.
	Definitive bool
}

// zaiUTC8 is the zone of every Z.AI reset stamp. The stamps carry no zone, and
// real captures prove UTC+8: the request IDs and the x-log-id header run on a
// UTC+8 clock, and every failure of a 1308 chain precedes the UTC+8 reading of
// its stamp while the first success follows it.
var zaiUTC8 = time.FixedZone("UTC+8", 8*60*60)

const (
	// stampSkew admits a reset stamp that lies slightly in the past, because
	// the vendor's clock and LeapMux's clock differ by up to this much.
	stampSkew = 60 * time.Second
	// maxResetAhead is the latest reset instant that a rule accepts.
	maxResetAhead = 31 * 24 * time.Hour
)

// ClassifyError reads vendor error text and returns a verdict. observedAt is
// the instant the failure happened, which limits every reset instant. It
// returns false when no rule matches. The precedence is A > C > B > D > E,
// and a blocked account decides right after rule C.
func ClassifyError(text string, observedAt time.Time) (Verdict, bool) {
	if strings.TrimSpace(text) == "" {
		return Verdict{}, false
	}
	normalized := normalize(text)
	for _, rule := range []func(string, time.Time) (Verdict, bool){
		ruleUsageLimitWithReset,
		ruleBillingBlock,
		ruleAccountBlock,
		ruleUsageLimitWithoutReset,
		ruleRateLimit,
		ruleTransient,
	} {
		if verdict, ok := rule(normalized, observedAt); ok {
			return verdict, true
		}
	}
	return Verdict{}, false
}

// ClassifyErrors reads separate error texts in their original order. The
// first definitive verdict wins; otherwise the first indicative verdict wins.
// Separate strings cannot combine a code in one field with a reset in another.
func ClassifyErrors(texts []string, observedAt time.Time) (Verdict, bool) {
	var first Verdict
	found := false
	for _, text := range texts {
		verdict, ok := ClassifyError(text, observedAt)
		if !ok {
			continue
		}
		if verdict.Definitive {
			return verdict, true
		}
		if !found {
			first, found = verdict, true
		}
	}
	return first, found
}

// --- Normalization ---

var (
	jsonEscapeRe = regexp.MustCompile(`\\(["\\/n])|\\u([0-9a-fA-F]{4})`)
	// zaiWrapperRe matches the [code][message][request_id] wrapper that the
	// Anthropic-compatible Z.AI endpoint puts around its message. The request
	// ID part is optional.
	zaiWrapperRe = regexp.MustCompile(`\[(\d{4})\]\[([^\]]*)\](?:\[[^\]]*\])?`)
)

// normalize decodes JSON escapes in place, replaces U+2019 with an ASCII
// apostrophe, and rewrites the Z.AI wrapper as `zaiWrapperCode=<code>
// <message>`, so the code keeps its evidence and the message reads as a plain
// sentence. No vendor writes that marker, so prose cannot imitate it.
func normalize(text string) string {
	text = jsonEscapeRe.ReplaceAllStringFunc(text, func(escape string) string {
		groups := jsonEscapeRe.FindStringSubmatch(escape)
		switch groups[1] {
		case `"`, `\`, `/`:
			return groups[1]
		case "n":
			return "\n"
		}
		if r, err := strconv.ParseUint(groups[2], 16, 32); err == nil {
			return string(rune(r))
		}
		return escape
	})
	text = strings.ReplaceAll(text, "’", "'")
	return zaiWrapperRe.ReplaceAllString(text, zaiWrapperCodeMarker+"$1 $2")
}

// --- Evidence helpers ---

// identifierRe compiles a matcher for one of several snake_case vendor codes.
// A code matches only as a whole identifier, so `usage_limit_reached` does not
// match inside `workspace_owner_usage_limit_reached`.
func identifierRe(codes ...string) *regexp.Regexp {
	quoted := make([]string, len(codes))
	for i, code := range codes {
		quoted[i] = regexp.QuoteMeta(code)
	}
	return regexp.MustCompile(`(?:^|[^A-Za-z0-9_])(` + strings.Join(quoted, "|") + `)(?:$|[^A-Za-z0-9_])`)
}

// zaiWrapperCodeMarker opens the code that normalize lifts out of the Z.AI
// wrapper.
const zaiWrapperCodeMarker = "zaiWrapperCode="

// zaiCodeRe finds a four-digit code in a code field: a JSON or Python-repr
// `code` key, or the marker that normalize writes for the wrapper. A provider
// that states the code in a suffix of its own, as Oh My Pi does with
// `(type=1308)`, keeps the vendor's sentence, which every Z.AI rule reads.
var zaiCodeRe = regexp.MustCompile(`(?:["']code["']\s*:\s*["']?|\b` + zaiWrapperCodeMarker + `)(\d{4})\b`)

// zaiCodes returns the four-digit codes that a code field of text states.
func zaiCodes(text string) map[string]bool {
	codes := make(map[string]bool)
	for _, match := range zaiCodeRe.FindAllStringSubmatch(text, -1) {
		codes[match[1]] = true
	}
	return codes
}

// hasAnyZAICode reports whether text states any of codes in a code field.
func hasAnyZAICode(text string, codes ...string) bool {
	stated := zaiCodes(text)
	for _, code := range codes {
		if stated[code] {
			return true
		}
	}
	return false
}

// keyValueRe compiles a matcher for `"key": "value"` in JSON or Python-repr
// form, for one of several values.
func keyValueRe(key string, values ...string) *regexp.Regexp {
	quoted := make([]string, len(values))
	for i, value := range values {
		quoted[i] = regexp.QuoteMeta(value)
	}
	return regexp.MustCompile(`["']` + regexp.QuoteMeta(key) + `["']\s*:\s*["'](` + strings.Join(quoted, "|") + `)["']`)
}

// firstMatch reports the first pattern of patterns that matches text.
func firstMatch(text string, patterns ...*regexp.Regexp) bool {
	for _, pattern := range patterns {
		if pattern.MatchString(text) {
			return true
		}
	}
	return false
}

// --- Rule A: a usage limit with a reset instant (Definitive) ---

var (
	// zaiResetRe matches Z.AI 1308 and 1310 in English.
	zaiResetRe = regexp.MustCompile(`(?i)(?:Usage limit reached for (\d{1,4}) (hours?|days?)|Weekly/Monthly Limit Exhausted)\.\s*Your limit will reset at (\d{4}-\d{2}-\d{2}) (\d{2}:\d{2}:\d{2})`)
	// zaiPastWindowResetRe matches Z.AI 1316 to 1321 in English.
	zaiPastWindowResetRe = regexp.MustCompile(`(?i)Usage limit reached for the past (5 hours|7 days)\.[^\]\n]{0,160}?Resets at (\d{4}-\d{2}-\d{2}) (\d{2}:\d{2}:\d{2})`)
	// zaiChineseResetRe matches Z.AI 1308, 1310, and 1316 to 1321 in Chinese.
	zaiChineseResetRe = regexp.MustCompile(`(?:使用上限|每周/每月使用上限)[^\]\n]{0,120}?您的限额将在\s*(\d{4}-\d{2}-\d{2})\s+(\d{2}:\d{2}:\d{2})\s*重置`)
	// zaiChineseWindowRe reads the window of a Chinese Z.AI usage limit.
	zaiChineseWindowRe = regexp.MustCompile(`(\d{1,4})\s*(小时|天)`)
	// anthropicSpendCapRe matches Anthropic's enforced spend limit.
	anthropicSpendCapRe = regexp.MustCompile(`You will regain access on (\d{4}-\d{2}-\d{2}) at (\d{2}:\d{2}) UTC`)

	chatGPTUsageLimitRe     = identifierRe("usage_limit_reached")
	chatGPTWorkspaceLimitRe = regexp.MustCompile(`workspace_(?:owner|member)_(?:credits_depleted|usage_limit_reached)`)
	chatGPTResetsAtRe       = regexp.MustCompile(`["']resets_at["']\s*:\s*(\d{9,11})\b`)
	chatGPTResetsInRe       = regexp.MustCompile(`["']resets_in_seconds["']\s*:\s*(\d{1,9})\b`)
	chatGPTWindowRe         = regexp.MustCompile(`["']limit_window_minutes["']\s*:\s*(\d{1,9})\b`)
)

func ruleUsageLimitWithReset(text string, observedAt time.Time) (Verdict, bool) {
	if match := zaiResetRe.FindStringSubmatch(text); match != nil {
		window := maxResetAhead // Weekly/Monthly.
		if match[1] != "" {
			window = zaiWindow(match[1], match[2])
		}
		if verdict, ok := zaiReset(match[3], match[4], window, observedAt); ok {
			return verdict, true
		}
	}
	if match := zaiPastWindowResetRe.FindStringSubmatch(text); match != nil {
		window := 5 * time.Hour
		if strings.EqualFold(match[1], "7 days") {
			window = 7 * 24 * time.Hour
		}
		if verdict, ok := zaiReset(match[2], match[3], window, observedAt); ok {
			return verdict, true
		}
	}
	if match := zaiChineseResetRe.FindStringSubmatch(text); match != nil {
		// The Chinese window precedes the matched clause: 已达到 5 小时的使用上限.
		if verdict, ok := zaiReset(match[1], match[2], zaiChineseWindow(text), observedAt); ok {
			return verdict, true
		}
	}
	if match := anthropicSpendCapRe.FindStringSubmatch(text); match != nil {
		resetAt, err := time.ParseInLocation("2006-01-02 15:04", match[1]+" "+match[2], time.UTC)
		if err == nil && plausibleReset(resetAt, maxResetAhead, observedAt) {
			return usageWithReset(resetAt, "Anthropic reported a spend limit that lifts at a stated time"), true
		}
	}
	if chatGPTUsageLimitRe.MatchString(text) && !chatGPTWorkspaceLimitRe.MatchString(text) {
		if resetAt, ok := chatGPTReset(text, observedAt); ok {
			return usageWithReset(resetAt, "ChatGPT reported a usage limit that lifts at a stated time"), true
		}
	}
	return Verdict{}, false
}

// zaiWindow converts the window of an English Z.AI usage limit to a duration.
func zaiWindow(count, unit string) time.Duration {
	n, err := strconv.Atoi(count)
	if err != nil || n <= 0 {
		return 0
	}
	if strings.HasPrefix(strings.ToLower(unit), "day") {
		return time.Duration(n) * 24 * time.Hour
	}
	return time.Duration(n) * time.Hour
}

// zaiChineseWindow reads the window of a Chinese Z.AI usage limit from the
// whole text. A weekly or monthly limit, and a text that states no window, may
// reset up to 31 days ahead.
func zaiChineseWindow(text string) time.Duration {
	match := zaiChineseWindowRe.FindStringSubmatch(text)
	if match == nil || strings.Contains(text, "每周/每月") {
		return maxResetAhead
	}
	n, err := strconv.Atoi(match[1])
	if err != nil || n <= 0 {
		return maxResetAhead
	}
	if match[2] == "天" {
		return time.Duration(n) * 24 * time.Hour
	}
	return time.Duration(n) * time.Hour
}

// zaiReset reads a Z.AI reset stamp as UTC+8 and checks that it falls inside
// the stated window. The stamp drops the sub-second part, so the retry waits
// one second past it. An implausible stamp gives no verdict, and rule B then
// decides from the window wording alone.
func zaiReset(date, clock string, window time.Duration, observedAt time.Time) (Verdict, bool) {
	stamp, err := time.ParseInLocation("2006-01-02 15:04:05", date+" "+clock, zaiUTC8)
	if err != nil || window <= 0 {
		return Verdict{}, false
	}
	resetAt := stamp.Add(time.Second).UTC()
	if !plausibleReset(resetAt, window, observedAt) {
		return Verdict{}, false
	}
	return usageWithReset(resetAt, "Z.AI reported a usage limit that lifts at a stated time"), true
}

// plausibleReset reports whether resetAt falls in
// (observedAt - stampSkew, observedAt + window + stampSkew].
func plausibleReset(resetAt time.Time, window time.Duration, observedAt time.Time) bool {
	return resetAt.After(observedAt.Add(-stampSkew)) && !resetAt.After(observedAt.Add(window+stampSkew))
}

// chatGPTReset reads the reset instant of a ChatGPT plan limit: resets_at in
// epoch seconds, or else resets_in_seconds from observedAt.
func chatGPTReset(text string, observedAt time.Time) (time.Time, bool) {
	if match := chatGPTResetsAtRe.FindStringSubmatch(text); match != nil {
		if epoch, err := strconv.ParseInt(match[1], 10, 64); err == nil {
			resetAt := time.Unix(epoch, 0).UTC()
			if resetAt.After(observedAt) && !resetAt.After(observedAt.Add(maxResetAhead)) {
				return resetAt, true
			}
		}
	}
	if match := chatGPTResetsInRe.FindStringSubmatch(text); match != nil {
		if seconds, err := strconv.ParseInt(match[1], 10, 64); err == nil && seconds > 0 {
			delay := time.Duration(seconds) * time.Second
			if delay <= maxResetAhead {
				return observedAt.Add(delay).UTC(), true
			}
		}
	}
	return time.Time{}, false
}

func usageWithReset(resetAt time.Time, cause string) Verdict {
	return Verdict{
		Failure:    providerkit.TurnFailure{Kind: providerkit.TurnFailureUsageLimited, ResetAt: resetAt, Cause: cause},
		Definitive: true,
	}
}

// --- Rule C: a billing block (Definitive) ---

var (
	billingCodeRe = identifierRe(
		"insufficient_quota",
		"credit_balance_exhausted",
		"organization_spend_limit_exceeded",
		"project_spend_limit_exceeded",
		"organization_usage_limit_exceeded",
		"usage_not_included",
		"exceeded_current_quota_error",
		"insufficient_balance",
		"membership_required",
		"credits_required",
		"billing_error",
		"access_terminated_error",
		"subscription_sharing_workplace_individual_usage_limit_exceeded",
	)
	billingSentenceRes = []*regexp.Regexp{
		regexp.MustCompile(`Your credit balance is too low`),
		regexp.MustCompile(`is suspended due to insufficient balance`),
		regexp.MustCompile(`exceeded your current token quota`),
		regexp.MustCompile(`Your credit balance is insufficient`),
		regexp.MustCompile(`Extra Usage balance has run out`),
		regexp.MustCompile(`reached the Extra Usage limit set for your account by your organization`),
		regexp.MustCompile(`out of extra usage`),
		regexp.MustCompile(`Usage credits are required`),
		regexp.MustCompile(`Extra usage is required`),
		regexp.MustCompile(`does not yet include access to`),
		regexp.MustCompile(`package has expired`),
		regexp.MustCompile(`subscription does not have access to`),
		regexp.MustCompile(`would exceed your account's monthly spend limit`),
		regexp.MustCompile(`does not comply with the Fair Usage Policy`),
		regexp.MustCompile(`This API Key is limited to enterprise coding package scenarios`),
		regexp.MustCompile(`Insufficient [Bb]alance`),
		regexp.MustCompile(`(?i)\bspend(?:ing)?\s+cap\b`),
		regexp.MustCompile(`未开通会员或会员已到期`),
		regexp.MustCompile(`您的账户已欠费`),
		regexp.MustCompile(`套餐已到期`),
		regexp.MustCompile(`暂未开放[^\n]{0,80}权限`),
		regexp.MustCompile(`企业套餐已失效`),
		regexp.MustCompile(`不符合公平使用策略`),
		regexp.MustCompile(`仅限企业编程套餐场景使用`),
	}
	// quotaSentenceRe is OpenAI's insufficient_quota sentence. Two vendors
	// reuse it for a rate limit: DashScope, with its code, for a token-per-minute
	// throttle that its #token-limit anchor marks; and the Gemini API for every
	// quota of a RESOURCE_EXHAUSTED 429, a per-minute one included, which its
	// quota metric and its rate-limit link mark.
	quotaSentenceRe = regexp.MustCompile(`You exceeded your current quota`)
	dashScopeRe     = regexp.MustCompile(`#token-limit`)
	geminiQuotaRe   = regexp.MustCompile(`Quota exceeded for metric|\bRESOURCE_EXHAUSTED\b|ai\.google\.dev/gemini-api/docs/rate-limits`)
	// geminiZeroQuotaRe matches a Gemini quota of zero, which Gemini CLI
	// 0.65.0 treats as not retryable (TerminalQuotaError): no wait lifts it.
	geminiZeroQuotaRe = regexp.MustCompile(`Quota exceeded for metric[^\n]*\blimit:\s*0(?:[^0-9.]|\.[^0-9]|$)`)
	// zaiBillingCodes are the Z.AI codes of a billing or account block.
	zaiBillingCodes = []string{"1113", "1309", "1311", "1313", "1314", "1315"}
)

func ruleBillingBlock(text string, _ time.Time) (Verdict, bool) {
	// A Z.AI usage limit whose stamp failed the plausibility check reaches
	// here. Z.AI 1316 and 1317 also state "Insufficient balance for extra
	// usage", which describes the overage and not the plan, so rule B decides.
	if zaiEnglishUsageRe.MatchString(text) || zaiChineseResetRe.MatchString(text) {
		return Verdict{}, false
	}
	if chatGPTUsageLimitRe.MatchString(text) && chatGPTWorkspaceLimitRe.MatchString(text) {
		return billingVerdict("ChatGPT reported a workspace credit or usage limit"), true
	}
	if geminiZeroQuotaRe.MatchString(text) {
		return billingVerdict("the Gemini API states a quota of zero"), true
	}
	quotaIsRateLimit := dashScopeRe.MatchString(text) || geminiQuotaRe.MatchString(text)
	for _, match := range billingCodeRe.FindAllStringSubmatch(text, -1) {
		if match[1] == "insufficient_quota" && quotaIsRateLimit {
			continue
		}
		return billingVerdict("the vendor reported " + match[1]), true
	}
	if !quotaIsRateLimit && quotaSentenceRe.MatchString(text) {
		return billingVerdict("the vendor reported an exceeded quota"), true
	}
	if hasAnyZAICode(text, zaiBillingCodes...) {
		return billingVerdict("Z.AI reported a billing or plan block"), true
	}
	for _, sentence := range billingSentenceRes {
		if sentence.MatchString(text) {
			return billingVerdict("the vendor reported a billing or plan block"), true
		}
	}
	return Verdict{}, false
}

func billingVerdict(cause string) Verdict {
	return Verdict{
		Failure:    providerkit.TurnFailure{Kind: providerkit.TurnFailureBillingBlocked, Cause: cause},
		Definitive: true,
	}
}

// accountBlockRes match a vendor's block of the account, which neither a wait
// nor a billing change lifts. Kimi's risk control sends "You've reached your
// concurrent request limit." with a 429, and only an appeal to Kimi lifts it.
var accountBlockRes = []*regexp.Regexp{
	regexp.MustCompile(`You've reached your concurrent request limit`),
}

// ruleAccountBlock gives a blocked account a definitive NotRetryable verdict:
// a retry fails as the turn did, and so does every later one.
func ruleAccountBlock(text string, _ time.Time) (Verdict, bool) {
	if !firstMatch(text, accountBlockRes...) {
		return Verdict{}, false
	}
	return Verdict{
		Failure:    providerkit.TurnFailure{Kind: providerkit.TurnFailureNotRetryable, Cause: "the vendor blocked the account"},
		Definitive: true,
	}, true
}

// --- Rule B: a usage limit with no reset instant ---

var (
	kimiFiveHourRe       = regexp.MustCompile(`(?i)\b5-hour usage limit\b`)
	kimiWeeklyRe         = regexp.MustCompile(`(?i)\bweekly \(7-day\) usage limit\b`)
	kimiMonthlyRe        = regexp.MustCompile(`(?i)\bmonthly usage limit\b`)
	mimoWeeklyRe         = regexp.MustCompile(`本周用量已满`)
	mimoWeeklyCodeRe     = identifierRe("subscription_quota_exhausted")
	anthropicAccountRe   = regexp.MustCompile(`would exceed your account's rate limit`)
	chatGPTSharingRe     = identifierRe("subscription_sharing_usage_limit_exceeded")
	geminiDailyQuotaRe   = regexp.MustCompile(`(?i)You have exhausted your daily quota on this model`)
	zaiEnglishUsageRe    = regexp.MustCompile(`(?i)Usage limit reached for (?:the past )?\d{1,4} (?:hours?|days?)|Weekly/Monthly Limit Exhausted`)
	zaiChineseUsageRe    = regexp.MustCompile(`(?:使用上限|每周/每月使用上限)`)
	chatGPTWeekInMinutes = int64(7 * 24 * 60)
)

func ruleUsageLimitWithoutReset(text string, _ time.Time) (Verdict, bool) {
	switch {
	case kimiFiveHourRe.MatchString(text):
		return usageWithoutReset("Kimi reported its 5-hour usage limit", providerkit.UsageWindowWeekOrLess), true
	case kimiWeeklyRe.MatchString(text):
		return usageWithoutReset("Kimi reported its weekly usage limit", providerkit.UsageWindowWeekOrLess), true
	case kimiMonthlyRe.MatchString(text):
		return usageWithoutReset("Kimi reported its monthly usage limit", providerkit.UsageWindowLonger), true
	case mimoWeeklyCodeRe.MatchString(text) || mimoWeeklyRe.MatchString(text):
		return usageWithoutReset("MiMo reported its weekly usage limit", providerkit.UsageWindowWeekOrLess), true
	case chatGPTUsageLimitRe.MatchString(text):
		return usageWithoutReset("ChatGPT reported a usage limit", chatGPTWindow(text)), true
	case anthropicAccountRe.MatchString(text):
		return usageWithoutReset("Anthropic reported an account rate limit", providerkit.UsageWindowUnstated), true
	case chatGPTSharingRe.MatchString(text):
		return usageWithoutReset("ChatGPT reported a shared-subscription usage limit", providerkit.UsageWindowUnstated), true
	case geminiDailyQuotaRe.MatchString(text):
		return usageWithoutReset("Gemini reported its daily quota", providerkit.UsageWindowWeekOrLess), true
	case zaiEnglishUsageRe.MatchString(text):
		// A Z.AI limit whose stamp failed the plausibility check. The window
		// comes from the limit's own clause: the 1318 and 1319 templates also
		// state a "monthly spend limit" that is not the window.
		return usageWithoutReset("Z.AI reported a usage limit with an implausible reset stamp", providerkit.UsageWindowOf(zaiEnglishUsageRe.FindString(text))), true
	case zaiChineseUsageRe.MatchString(text) && zaiChineseResetRe.MatchString(text):
		return usageWithoutReset("Z.AI reported a usage limit with an implausible reset stamp", zaiChineseUsageWindow(text)), true
	}
	return Verdict{}, false
}

// chatGPTWindow reads limit_window_minutes. A window of a week or less gives
// UsageWindowWeekOrLess, and an absent or longer window gives Unstated.
func chatGPTWindow(text string) providerkit.UsageWindow {
	match := chatGPTWindowRe.FindStringSubmatch(text)
	if match == nil {
		return providerkit.UsageWindowUnstated
	}
	minutes, err := strconv.ParseInt(match[1], 10, 64)
	if err != nil || minutes <= 0 || minutes > chatGPTWeekInMinutes {
		return providerkit.UsageWindowUnstated
	}
	return providerkit.UsageWindowWeekOrLess
}

// zaiChineseUsageWindow classifies the window of a Chinese Z.AI usage limit.
func zaiChineseUsageWindow(text string) providerkit.UsageWindow {
	window := zaiChineseWindow(text)
	if window == maxResetAhead {
		return providerkit.UsageWindowLonger
	}
	if window <= 7*24*time.Hour {
		return providerkit.UsageWindowWeekOrLess
	}
	return providerkit.UsageWindowLonger
}

func usageWithoutReset(cause string, window providerkit.UsageWindow) Verdict {
	return Verdict{Failure: providerkit.TurnFailure{Kind: providerkit.TurnFailureUsageLimited, Window: window, Cause: cause}}
}

// --- Rule D: a rate limit ---

var rateLimitRes = []*regexp.Regexp{
	regexp.MustCompile(`Rate limit reached for requests`),
	regexp.MustCompile(`您的账户已达到速率限制`),
	identifierRe("rate_limit_exceeded", "slow_down", "rate_limit_reached_error"),
	regexp.MustCompile(`Rate limit reached for \S+ in organization`),
	regexp.MustCompile(`(?i)\brequest reached [^\n]{0,80}\b(?:RPM|TPM|TPD|concurrency)\b`),
	regexp.MustCompile(`We're receiving too many requests`),
	regexp.MustCompile(`\bRate Limit Reached\b`),
	regexp.MustCompile(`\bper-minute rate limit\b`),
	dashScopeRe,
}

func ruleRateLimit(text string, _ time.Time) (Verdict, bool) {
	// A Gemini quota is a rate limit, but a capacity failure that the Gemini
	// API also sends as a RESOURCE_EXHAUSTED 429 is transient (rule E).
	geminiQuota := geminiQuotaRe.MatchString(text) && !capacityRe.MatchString(text)
	if geminiQuota || hasAnyZAICode(text, "1302") || firstMatch(text, rateLimitRes...) {
		return Verdict{Failure: providerkit.TurnFailure{Kind: providerkit.TurnFailureRateLimited, Cause: "the vendor reported a rate limit"}}, true
	}
	return Verdict{}, false
}

// --- Rule E: a transient failure ---

// capacityRe matches a model capacity failure. MODEL_CAPACITY also opens
// MODEL_CAPACITY_EXHAUSTED.
var capacityRe = regexp.MustCompile(`(?i)no capacity available|capacity exceeded|\bMODEL_CAPACITY`)

var transientRes = []*regexp.Regexp{
	regexp.MustCompile(`The service may be temporarily overloaded`),
	regexp.MustCompile(`该模型当前访问量过大`),
	regexp.MustCompile(`Network error, error id`),
	regexp.MustCompile(`网络错误，错误id`),
	regexp.MustCompile(`The server had an error while processing your request`),
	identifierRe("server_is_overloaded", "service_unavailable_error", "engine_overloaded_error", "overloaded_error", "ServiceUnavailableError"),
	regexp.MustCompile(`The engine is currently overloaded`),
	keyValueRe("type", "api_error", "timeout_error"),
	// DeepSeek's whole message, at the start of the text or of a quoted value.
	// The plain HTTP phrase "Internal Server Error" decides nothing: the
	// provider's own status rule reads a 5xx.
	regexp.MustCompile(`(?:^|["':]\s*)Server (?:Error|Overloaded)\b`),
	regexp.MustCompile(`服务器负载过高`),
	regexp.MustCompile(`Connection prematurely closed BEFORE response`),
	keyValueRe("status", "INTERNAL", "UNAVAILABLE", "DEADLINE_EXCEEDED"),
	capacityRe,
}

func ruleTransient(text string, _ time.Time) (Verdict, bool) {
	if hasAnyZAICode(text, "1305", "1234", "1200", "1230") || firstMatch(text, transientRes...) {
		return Verdict{Failure: providerkit.TurnFailure{Kind: providerkit.TurnFailureTransient, Cause: "the vendor reported a transient failure"}}, true
	}
	return Verdict{}, false
}
