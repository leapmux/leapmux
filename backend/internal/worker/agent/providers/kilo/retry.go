package kilo

import (
	"encoding/json"

	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/opencode"
)

// Codes of the Kilo gateway (R/opencode-kilo-mimo.md) in error.code of a
// response body. Each states a limit that only the reader's own action lifts:
// a sign-in for a paid model, or a plan for a spent promotion. The gateway
// states no reset for either, and Kilo never retries them.
const (
	kiloCodePaidModelAuthRequired      = "PAID_MODEL_AUTH_REQUIRED"
	kiloCodePromotionModelLimitReached = "PROMOTION_MODEL_LIMIT_REACHED"
)

// kiloMemberFailure is the MemberFailure hook of Kilo. It gives NotRetryable
// for the two codes of the Kilo gateway above, and leaves every other error to
// the family recipe.
func kiloMemberFailure(failure opencode.DaemonError) (providerkit.TurnFailure, bool) {
	var body struct {
		Error struct {
			// Code is any, because another gateway states a number here.
			Code any `json:"code"`
		} `json:"error"`
	}
	if failure.Data.ResponseBody == "" || json.Unmarshal([]byte(failure.Data.ResponseBody), &body) != nil {
		return providerkit.TurnFailure{}, false
	}
	switch code, _ := body.Error.Code.(string); code {
	case kiloCodePaidModelAuthRequired, kiloCodePromotionModelLimitReached:
		return providerkit.TurnFailure{Kind: providerkit.TurnFailureNotRetryable, Cause: "the Kilo gateway reported " + code}, true
	}
	return providerkit.TurnFailure{}, false
}
