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
	assert.Equal(t, "LeastPrivilege", definition.Principals.Items[0].RunLevel)
	require.Len(t, definition.Triggers.Items, 1)
	assert.Equal(t, spec.AccountID, definition.Triggers.Items[0].UserID)
	assert.True(t, definition.matches(spec, windowsTaskIdentity{Arguments: spec.RunArguments(), PrincipalSID: spec.AccountID, TriggerSID: spec.AccountID}))
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
			value.Principals.Items[0].RunLevel = "HighestAvailable"
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
