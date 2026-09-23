package acp

// text.go holds the ACP option-label helper. It depends on nothing but the
// standard library.

import (
	"strings"
)

// normalizeOptionName trims an ACP option's server-reported display name and
// treats a blank or id-equal name as absent (""), so providerkit.TitleCaseID falls back to
// title-casing the id. Applied on both the handshake (buildPrimaryAgentOptions)
// and runtime (buildConfigOptionSelect) option-building paths so an option renders
// identically regardless of which path produced it -- OpenCode-family agents often
// report name == id or whitespace-only names.
func normalizeOptionName(name, id string) string {
	name = strings.TrimSpace(name)
	if name == id {
		return ""
	}
	return name
}
