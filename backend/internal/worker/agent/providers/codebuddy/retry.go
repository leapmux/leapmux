package codebuddy

import (
	"encoding/json"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/modelvendor"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
)

// codebuddyErrorInfo is one entry of a result's errors_info. CodeBuddy builds
// it from the error that ended the turn. Every field is optional. An absent
// status reads as 0, which is no HTTP status. The code is a gateway business
// code (a number) or a provider's code (a string, such as "cbc_empty_error").
type codebuddyErrorInfo struct {
	Status   int             `json:"status"`
	Code     json.RawMessage `json:"code"`
	Category string          `json:"category"`
	Details  string          `json:"details"`
}

// numericCode returns the business code, and false when the entry states no
// numeric code. An absent code is not the quota code 0.
func (info *codebuddyErrorInfo) numericCode() (int64, bool) {
	code, err := strconv.ParseInt(string(info.Code), 10, 64)
	return code, err == nil
}

// stringCode returns the code when the entry states it as a string.
func (info *codebuddyErrorInfo) stringCode() string {
	var code string
	if json.Unmarshal(info.Code, &code) != nil {
		return ""
	}
	return code
}

// The categories that CodeBuddy derives from a status when the error states
// none: 408, 502, 503, and 504 read as network, 429 as quota, and every other
// status of 500 or more as model_service.
const (
	codebuddyCategoryNetwork      = "network"
	codebuddyCategoryModelService = "model_service"
	codebuddyCategoryQuota        = "quota"
)

// The gateway business codes, by verdict. A license code states an expired
// or inactive license, and a usage window code states a windowed limit with
// no reset instant.
var (
	codebuddyBillingCodes     = map[int64]bool{14001: true, 14012: true, 14013: true, 14014: true, 14018: true, 14019: true}
	codebuddyLicenseCodes     = map[int64]bool{14015: true, 14016: true, 14017: true}
	codebuddyUsageWindowCodes = map[int64]bool{6003: true, 6004: true, 6007: true, 6008: true}
	codebuddyRateLimitedCodes = map[int64]bool{0: true, 14003: true, 6001: true, 6002: true, 6005: true, 6006: true}
)

// codebuddyTransientPrefixes are the first errors of a stream failure that
// CodeBuddy states with no structured error.
var codebuddyTransientPrefixes = []string{"Stream timeout: no data received", "Empty stream:"}

// codebuddyBillingTextRe matches the exhausted-balance texts of CodeBuddy
// 2.175.0's own list (`Ic` in its bundle). They arrive with the quota code 0,
// which alone reads as a rate limit, and the vendor parser knows none of them.
var codebuddyBillingTextRe = regexp.MustCompile(`(?i)(?:^|[^a-z0-9_])(?:quota exhausted|(?:no available|insufficient) credits?|credits? exhausted|billing_hard_limit_reached)(?:[^a-z0-9_]|$)|额度已?用尽|额度不足`)

// codebuddyResultFailure decides the verdict of one result. Only a failed
// result of a turn that the user did not stop can retry. observedAt limits a
// reset instant that a vendor's text states.
func codebuddyResultFailure(result resultMessage, stopRequested bool, observedAt time.Time) providerkit.TurnFailure {
	if !result.IsError || stopRequested {
		return providerkit.TurnFailure{}
	}
	info := result.firstErrorInfo()
	vendor, vendorMatched := codebuddyVendorVerdict(result, info, observedAt)
	if vendorMatched && vendor.Definitive {
		return vendor.Failure
	}
	if codebuddyStatesBilling(result, info) {
		return providerkit.TurnFailure{Kind: providerkit.TurnFailureBillingBlocked, Cause: "CodeBuddy reported an exhausted balance"}
	}
	if info != nil {
		if failure, ok := codebuddyDefinitiveCodeFailure(info); ok {
			return failure
		}
	}
	// The vendor's own wording is more specific than a status or the quota
	// code 0: a usage limit with a monthly window must not retry.
	if vendorMatched {
		return vendor.Failure
	}
	if info == nil {
		if len(result.Errors) > 0 && hasAnyPrefix(result.Errors[0], codebuddyTransientPrefixes) {
			return providerkit.TurnFailure{Kind: providerkit.TurnFailureTransient, Cause: "CodeBuddy reported a transient failure"}
		}
	} else if failure, ok := codebuddyRetryableClassFailure(info); ok {
		return failure
	}
	return providerkit.TurnFailure{}
}

// codebuddyStatesBilling reports whether a text of the failed result states an
// exhausted balance in CodeBuddy's own words.
func codebuddyStatesBilling(result resultMessage, info *codebuddyErrorInfo) bool {
	if len(result.Errors) > 0 && codebuddyBillingTextRe.MatchString(result.Errors[0]) {
		return true
	}
	return info != nil && codebuddyBillingTextRe.MatchString(info.Details)
}

// codebuddyDefinitiveCodeFailure classifies the business codes that decide on
// their own: a billing code, a license code, and a usage window code.
func codebuddyDefinitiveCodeFailure(info *codebuddyErrorInfo) (providerkit.TurnFailure, bool) {
	code, hasCode := info.numericCode()
	switch {
	case hasCode && codebuddyBillingCodes[code]:
		return providerkit.TurnFailure{Kind: providerkit.TurnFailureBillingBlocked, Cause: "CodeBuddy reported billing code " + strconv.FormatInt(code, 10)}, true
	case hasCode && codebuddyLicenseCodes[code]:
		return providerkit.TurnFailure{}, true
	case hasCode && codebuddyUsageWindowCodes[code]:
		return providerkit.TurnFailure{Kind: providerkit.TurnFailureUsageLimited, Window: providerkit.UsageWindowWeekOrLess, Cause: "CodeBuddy reported a windowed usage limit"}, true
	}
	return providerkit.TurnFailure{}, false
}

// codebuddyRetryableClassFailure classifies the categories and statuses that
// LeapMux retries.
func codebuddyRetryableClassFailure(info *codebuddyErrorInfo) (providerkit.TurnFailure, bool) {
	code, hasCode := info.numericCode()
	switch {
	case info.Category == codebuddyCategoryNetwork, info.Category == codebuddyCategoryModelService,
		info.Status == 408, info.Status >= 500 && info.Status <= 599 && (info.Status < 570 || info.Status > 579):
		return providerkit.TurnFailure{Kind: providerkit.TurnFailureTransient, Cause: "CodeBuddy reported a transient failure"}, true
	case info.Category == codebuddyCategoryQuota && hasCode && codebuddyRateLimitedCodes[code]:
		// A rate-limit code counts only in the quota category: the 400 context
		// and 401 probes also carry the code 0, in other categories.
		return providerkit.TurnFailure{Kind: providerkit.TurnFailureRateLimited, Cause: "CodeBuddy reported a rate limit"}, true
	}
	return providerkit.TurnFailure{}, false
}

// codebuddyVendorVerdict reads the vendor's verdict from the texts of a
// failed result: the first error, the entry's details, and a string code. A
// definitive verdict of any text wins over an indicative one.
func codebuddyVendorVerdict(result resultMessage, info *codebuddyErrorInfo, observedAt time.Time) (modelvendor.Verdict, bool) {
	var texts []string
	if len(result.Errors) > 0 {
		texts = append(texts, result.Errors[0])
	}
	if info != nil {
		texts = append(texts, info.Details, info.stringCode())
	}
	var indicative modelvendor.Verdict
	found := false
	for _, text := range texts {
		verdict, ok := modelvendor.ClassifyError(text, observedAt)
		if !ok {
			continue
		}
		if verdict.Definitive {
			return verdict, true
		}
		if !found {
			indicative, found = verdict, true
		}
	}
	return indicative, found
}

// firstErrorInfo returns the first entry of errors_info that is not null, as
// CodeBuddy's own reader does, or nil when there is none.
func (r *resultMessage) firstErrorInfo() *codebuddyErrorInfo {
	for _, info := range r.ErrorsInfo {
		if info != nil {
			return info
		}
	}
	return nil
}

// hasAnyPrefix reports whether text starts with any of prefixes.
func hasAnyPrefix(text string, prefixes []string) bool {
	for _, prefix := range prefixes {
		if strings.HasPrefix(text, prefix) {
			return true
		}
	}
	return false
}
