package launch

import (
	"crypto/sha256"
	"fmt"
	"strconv"
	"strings"
)

const environmentPinPrefix = "__LEAPMUX_PIN_"

// environmentPin transports one value without putting it in shell code.
type environmentPin struct {
	target string
	source string
	value  string
}

// shellLaunchSpec keeps the public settings with their prepared environment references.
type shellLaunchSpec struct {
	WrapSpec
	pins []environmentPin
}

func prepareShellLaunch(dialect shellDialect, spec WrapSpec, token string, environ []string) shellLaunchSpec {
	used := make(map[string]bool, len(environ)+len(spec.SetEnv)+len(spec.StripEnvKeys))
	for _, entry := range environ {
		name, _, _ := strings.Cut(entry, "=")
		used[strings.ToUpper(name)] = true
	}
	for _, entry := range spec.SetEnv {
		name, _, _ := strings.Cut(entry, "=")
		used[strings.ToUpper(name)] = true
	}
	for _, name := range spec.StripEnvKeys {
		used[strings.ToUpper(name)] = true
	}
	prepared := shellLaunchSpec{WrapSpec: spec}
	for i, entry := range spec.SetEnv {
		name, value, _ := strings.Cut(entry, "=")
		alias := environmentPinPrefix + token + "_" + strconv.Itoa(i)
		if !shellIdentifier.MatchString(alias) {
			panic(fmt.Sprintf("shell wrapper: pin source %q is not a plain identifier", alias))
		}
		alias = dialect.pinSource(alias, used)
		used[strings.ToUpper(alias)] = true
		prepared.pins = append(prepared.pins, environmentPin{target: name, source: alias, value: value})
	}
	return prepared
}

// A collision must never grow a pin past BSD csh's substitution limit.
func (cshDialect) pinSource(base string, used map[string]bool) string {
	const hashBytes = (cshVariableNameLimit - len(environmentPinPrefix)) / 2
	for attempt := 0; ; attempt++ {
		digest := sha256.Sum256([]byte(base + ":" + strconv.Itoa(attempt)))
		source := fmt.Sprintf("%s%x", environmentPinPrefix, digest[:hashBytes])
		if !used[strings.ToUpper(source)] {
			return source
		}
	}
}

func (s shellLaunchSpec) pinEnvironment(environ []string) []string {
	for _, pin := range s.pins {
		environ = append(environ, pin.source+"="+pin.value)
	}
	return environ
}
