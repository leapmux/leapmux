package cmd

import (
	"strings"

	"google.golang.org/protobuf/reflect/protoreflect"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/util/agentlabels"
)

// Every enum in a result prints as one lowercase token, which a flag that
// takes the same enum accepts back:
//
//   - A proto enum prints its value name without the enum's own prefix:
//     NODE_KIND_LEAF is "leaf", and AGENT_ACTIVITY_STATE_WAITING_FOR_USER is
//     "waiting_for_user". enumToken derives the token from the descriptor,
//     so a new enum value needs no table here.
//   - A TabType prints the token of contracts/tab-types.json via
//     resolve.TabTypeWireName, which the frontend shares.
//     TestEnumTokenMatchesTheTabTypeContract keeps the two rules in agreement.
//   - An AgentProvider prints its canonical CLI alias from agentlabels,
//     which --provider accepts.
//
// The zero value of every enum prints "", so `omitempty` drops an unset
// field.

// enumToken returns the lowercase token of a proto enum value.
func enumToken(e protoreflect.Enum) string {
	if e == nil || e.Number() == 0 {
		return ""
	}
	desc := e.Descriptor()
	value := desc.Values().ByNumber(e.Number())
	if value == nil {
		return ""
	}
	return strings.ToLower(strings.TrimPrefix(string(value.Name()), enumPrefix(desc)))
}

// enumPrefix is the prefix that every value name of the enum shares: the
// name of its zero value without "UNSPECIFIED". TAB_TYPE_UNSPECIFIED gives
// "TAB_TYPE_".
func enumPrefix(desc protoreflect.EnumDescriptor) string {
	zero := desc.Values().ByNumber(0)
	if zero == nil {
		return ""
	}
	return strings.TrimSuffix(string(zero.Name()), "UNSPECIFIED")
}

// enumTokens lists the token of every value of the enum but the zero value,
// in declaration order, for the help of a flag that takes the enum.
func enumTokens(e protoreflect.Enum) []string {
	desc := e.Descriptor()
	values := desc.Values()
	prefix := enumPrefix(desc)
	out := make([]string, 0, values.Len())
	for i := range values.Len() {
		v := values.Get(i)
		if v.Number() == 0 {
			continue
		}
		out = append(out, strings.ToLower(strings.TrimPrefix(string(v.Name()), prefix)))
	}
	return out
}

// parseEnumToken returns the number of the value whose token is token, and
// false for an empty or unknown token.
func parseEnumToken(e protoreflect.Enum, token string) (protoreflect.EnumNumber, bool) {
	if token == "" {
		return 0, false
	}
	desc := e.Descriptor()
	values := desc.Values()
	prefix := enumPrefix(desc)
	for i := range values.Len() {
		v := values.Get(i)
		if v.Number() != 0 && strings.EqualFold(strings.TrimPrefix(string(v.Name()), prefix), token) {
			return v.Number(), true
		}
	}
	return 0, false
}

func agentProviderName(p leapmuxv1.AgentProvider) string {
	if p == leapmuxv1.AgentProvider_AGENT_PROVIDER_UNSPECIFIED {
		return ""
	}
	return agentlabels.DisplayName(p)
}

// kindLabel returns the token of a NodeKind for a message: "leaf",
// "split" or "grid".
func kindLabel(k leapmuxv1.NodeKind) string {
	if token := enumToken(k); token != "" {
		return token
	}
	// A message that prints the kind must not lose it: "tile X is a  node"
	// says nothing.
	return "unknown"
}
