package servicelaunch

import (
	"encoding/xml"
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestTaskDefinitionUsesPasswordAndOneAccount(t *testing.T) {
	spec, err := LaunchSpecFromService(testSpec())
	require.NoError(t, err)
	spec.AccountID = "S-1-5-21-1001"
	spec.Executable = `C:\Program Files\LeapMux & Tools\leapmux.exe`
	spec.WorkingDirectory = `C:\Users\A User`
	spec.RuntimeEnvironment = map[string]string{"API_TOKEN": "private-password"}
	data, err := taskDefinitionXML(spec, strings.Join(spec.RunArguments(), " "))
	require.NoError(t, err)
	assert.NotContains(t, string(data), "private-password")
	assert.Contains(t, string(data), "LeapMux &amp; Tools")
	var definition windowsTaskDefinition
	require.NoError(t, xml.Unmarshal(data, &definition))
	require.Len(t, definition.Principals.Items, 1)
	assert.Equal(t, "Password", definition.Principals.Items[0].LogonType)
	require.NotNil(t, definition.Principals.Items[0].RunLevel)
	assert.Equal(t, "LeastPrivilege", *definition.Principals.Items[0].RunLevel)
	require.Len(t, definition.Triggers.Items, 1)
	assert.Equal(t, spec.AccountID, definition.Triggers.Items[0].UserID)
	assert.True(t, definition.matches(spec, windowsTaskIdentity{Arguments: spec.RunArguments(), PrincipalSID: spec.AccountID, TriggerSID: spec.AccountID}))
}

func TestTaskDefinitionCarriesEveryLaunchField(t *testing.T) {
	base, err := LaunchSpecFromService(testSpec())
	require.NoError(t, err)
	base.AccountID = "S-1-5-21-1001"
	render := func(spec LaunchSpec) []byte {
		// upsertWindowsTask composes RunArguments with
		// windows.ComposeCommandLine, which builds only on Windows. Both
		// spellings read the same fields.
		definition, err := taskDefinitionXML(spec, strings.Join(spec.RunArguments(), " "))
		require.NoError(t, err)
		return append([]byte(taskName(spec)+"\n"), definition...)
	}
	assertDefinitionCarries(t, base, render, fieldsCarriedBy(map[string]bool{
		"InstanceID": true,
		"Mode":       true,
		// The startup kind selects the mechanism: windowsAdapter sends a
		// boot specification to the service, so a task always has login
		// startup, and validateWindowsLaunch refuses any other kind.
		"Startup":   false,
		"AccountID": true,
		// The task identifies the account by AccountID. RegisterTask takes
		// AccountName only to log on with the password, and
		// verifyWindowsAccount requires AccountName to resolve to AccountID.
		"AccountName":      false,
		"Executable":       true,
		"WorkingDirectory": true,
		"DataDirectory":    true,
	}))
}

func TestTaskDefinitionDetectsEffectiveLaunchDrift(t *testing.T) {
	spec, err := LaunchSpecFromService(testSpec())
	require.NoError(t, err)
	spec.AccountID = "S-1-5-21-1001"
	data, err := taskDefinitionXML(spec, strings.Join(spec.RunArguments(), " "))
	require.NoError(t, err)
	for name, mutate := range map[string]func(*windowsTaskDefinition, *windowsTaskIdentity){
		"account": func(value *windowsTaskDefinition, identity *windowsTaskIdentity) {
			value.Principals.Items[0].UserID = "S-1-5-21-1002"
			identity.PrincipalSID = "S-1-5-21-1002"
		},
		"trigger account": func(value *windowsTaskDefinition, identity *windowsTaskIdentity) {
			value.Triggers.Items[0].UserID = "S-1-5-21-1002"
			identity.TriggerSID = "S-1-5-21-1002"
		},
		"interactive token": func(value *windowsTaskDefinition, _ *windowsTaskIdentity) {
			value.Principals.Items[0].LogonType = "InteractiveToken"
		},
		"S4U": func(value *windowsTaskDefinition, _ *windowsTaskIdentity) {
			value.Principals.Items[0].LogonType = "S4U"
		},
		"elevated": func(value *windowsTaskDefinition, _ *windowsTaskIdentity) {
			value.Principals.Items[0].RunLevel = new("HighestAvailable")
		},
		"disabled":        func(value *windowsTaskDefinition, _ *windowsTaskIdentity) { value.Settings.Enabled = new(false) },
		"battery":         func(value *windowsTaskDefinition, _ *windowsTaskIdentity) { value.Settings.StopOnBattery = new(true) },
		"omitted battery": func(value *windowsTaskDefinition, _ *windowsTaskIdentity) { value.Settings.StopOnBattery = nil },
		"hard terminate": func(value *windowsTaskDefinition, _ *windowsTaskIdentity) {
			value.Settings.AllowHardTerminate = new(true)
		},
		"time limit": func(value *windowsTaskDefinition, _ *windowsTaskIdentity) { value.Settings.ExecutionLimit = "PT72H" },
		"restart": func(value *windowsTaskDefinition, _ *windowsTaskIdentity) {
			value.Settings.Restart = &windowsTaskRestart{Interval: "PT1M", Count: 3}
		},
		"extra action": func(value *windowsTaskDefinition, _ *windowsTaskIdentity) {
			value.Actions.Items = append(value.Actions.Items, value.Actions.Items[0])
		},
		"extra trigger": func(value *windowsTaskDefinition, _ *windowsTaskIdentity) {
			value.Triggers.Items = append(value.Triggers.Items, value.Triggers.Items[0])
		},
		"delayed trigger": func(value *windowsTaskDefinition, _ *windowsTaskIdentity) { value.Triggers.Items[0].Delay = "PT1H" },
		"different executable": func(value *windowsTaskDefinition, _ *windowsTaskIdentity) {
			value.Actions.Items[0].Command = "/another/executable"
		},
		"different directory": func(value *windowsTaskDefinition, _ *windowsTaskIdentity) {
			value.Actions.Items[0].WorkingDirectory = "/another/directory"
		},
		"different arguments": func(value *windowsTaskDefinition, identity *windowsTaskIdentity) {
			identity.Arguments = []string{"worker", "start"}
			value.Actions.Items[0].Arguments = strings.Join(identity.Arguments, " ")
		},
	} {
		t.Run(name, func(t *testing.T) {
			var definition windowsTaskDefinition
			require.NoError(t, xml.Unmarshal(data, &definition))
			identity := windowsTaskIdentity{Arguments: spec.RunArguments(), PrincipalSID: spec.AccountID, TriggerSID: spec.AccountID}
			mutate(&definition, &identity)
			assert.False(t, definition.matches(spec, identity))
		})
	}
}

func TestTaskDefinitionMatchesEquivalentNamespacePrefix(t *testing.T) {
	spec, err := LaunchSpecFromService(testSpec())
	require.NoError(t, err)
	data, err := taskDefinitionXML(spec, strings.Join(spec.RunArguments(), " "))
	require.NoError(t, err)
	prefixed := strings.ReplaceAll(string(data), "xmlns=", "xmlns:t=")
	prefixed = strings.ReplaceAll(prefixed, "</", "</t:")
	prefixed = regexp.MustCompile(`<([A-Za-z])`).ReplaceAllString(prefixed, `<t:$1`)
	var definition windowsTaskDefinition
	require.NoError(t, xml.Unmarshal([]byte(prefixed), &definition))
	assert.True(t, definition.matches(spec, windowsTaskIdentity{Arguments: spec.RunArguments(), PrincipalSID: spec.AccountID, TriggerSID: spec.AccountID}))
}

func TestTaskDefinitionParsesDecodedCOMUnicodeText(t *testing.T) {
	spec, err := LaunchSpecFromService(testSpec())
	require.NoError(t, err)
	spec.AccountID = "S-1-5-21-1001"
	spec.Executable = `C:\工具 & Scripts\leapmux.exe`
	spec.WorkingDirectory = `C:\Users\例子`
	data, err := taskDefinitionXML(spec, strings.Join(spec.RunArguments(), " "))
	require.NoError(t, err)
	for _, encoding := range []string{"", "UTF-8", "UTF-16", "utf-16"} {
		name := encoding
		if name == "" {
			name = "no declaration"
		}
		t.Run(name, func(t *testing.T) {
			document := string(data)
			if encoding != "" {
				document = `<?xml version="1.0" encoding="` + encoding + `"?>` + document
			}
			definition, err := parseWindowsTaskDefinition(document)
			require.NoError(t, err)
			require.Len(t, definition.Actions.Items, 1)
			assert.Equal(t, spec.Executable, definition.Actions.Items[0].Command)
			assert.Equal(t, spec.WorkingDirectory, definition.Actions.Items[0].WorkingDirectory)
			assert.True(t, definition.matches(spec, windowsTaskIdentity{Arguments: spec.RunArguments(), PrincipalSID: spec.AccountID, TriggerSID: spec.AccountID}))
		})
	}
}

func TestTaskDefinitionRejectsInvalidDecodedText(t *testing.T) {
	for _, document := range []string{"", `<Task>`, `<Other/>`, `<?xml version="1.0" encoding="windows-1252"?><Task/>`, `<Task>&unknown;</Task>`} {
		_, err := parseWindowsTaskDefinition(document)
		require.Error(t, err)
	}
}

func TestTaskDefinitionMatchesNativeOmittedDefaults(t *testing.T) {
	spec, err := LaunchSpecFromService(testSpec())
	require.NoError(t, err)
	spec.AccountID = "S-1-5-21-1001"
	data, err := taskDefinitionXML(spec, strings.Join(spec.RunArguments(), " "))
	require.NoError(t, err)
	document := string(data)
	for _, element := range []string{
		"<RunLevel>LeastPrivilege</RunLevel>",
		"<Enabled>true</Enabled>",
		"<StartWhenAvailable>false</StartWhenAvailable>",
		"<RunOnlyIfNetworkAvailable>false</RunOnlyIfNetworkAvailable>",
		"<RunOnlyIfIdle>false</RunOnlyIfIdle>",
		"<AllowStartOnDemand>true</AllowStartOnDemand>",
		"<Hidden>false</Hidden>",
	} {
		require.Contains(t, document, element)
		document = strings.ReplaceAll(document, element, "")
	}
	definition, err := parseWindowsTaskDefinition(`<?xml version="1.0" encoding="UTF-16"?>` + document)
	require.NoError(t, err)
	identity := windowsTaskIdentity{Arguments: spec.RunArguments(), PrincipalSID: spec.AccountID, TriggerSID: spec.AccountID}
	assert.True(t, definition.matches(spec, identity), "native omission of effective defaults preserves the installed policy")
	for _, invalid := range []string{"", "HighestAvailable", "Unknown", " LeastPrivilege "} {
		changed := strings.Replace(string(data), "<RunLevel>LeastPrivilege</RunLevel>", "<RunLevel>"+invalid+"</RunLevel>", 1)
		parsed, err := parseWindowsTaskDefinition(changed)
		require.NoError(t, err)
		assert.False(t, parsed.matches(spec, identity), "an explicit invalid or elevated run level stays a mismatch")
	}
}
