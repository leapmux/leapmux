package servicelaunch

import (
	"encoding/xml"
	"fmt"
	"io"
	"path"
	"slices"
	"strings"
)

const windowsTaskNamespace = "http://schemas.microsoft.com/windows/2004/02/mit/task"

type windowsTaskDefinition struct {
	XMLName      xml.Name                `xml:"Task"`
	Namespace    string                  `xml:"xmlns,attr"`
	Version      string                  `xml:"version,attr"`
	Registration windowsTaskRegistration `xml:"RegistrationInfo"`
	Triggers     windowsTaskTriggers     `xml:"Triggers"`
	Principals   windowsTaskPrincipals   `xml:"Principals"`
	Settings     windowsTaskSettings     `xml:"Settings"`
	Actions      windowsTaskActions      `xml:"Actions"`
}

type windowsTaskRegistration struct {
	Description string `xml:"Description"`
}
type windowsTaskTriggers struct {
	Items []windowsTaskTrigger `xml:",any"`
}
type windowsTaskTrigger struct {
	XMLName       xml.Name
	Enabled       *bool  `xml:"Enabled"`
	UserID        string `xml:"UserId"`
	Delay         string `xml:"Delay,omitempty"`
	StartBoundary string `xml:"StartBoundary,omitempty"`
	EndBoundary   string `xml:"EndBoundary,omitempty"`
}
type windowsTaskPrincipals struct {
	Items []windowsTaskPrincipal `xml:"Principal"`
}
type windowsTaskPrincipal struct {
	ID        string  `xml:"id,attr"`
	UserID    string  `xml:"UserId"`
	LogonType string  `xml:"LogonType"`
	RunLevel  *string `xml:"RunLevel"`
}
type windowsTaskSettings struct {
	MultipleInstances  string              `xml:"MultipleInstancesPolicy"`
	DisallowBattery    *bool               `xml:"DisallowStartIfOnBatteries"`
	StopOnBattery      *bool               `xml:"StopIfGoingOnBatteries"`
	AllowHardTerminate *bool               `xml:"AllowHardTerminate"`
	StartWhenAvailable *bool               `xml:"StartWhenAvailable"`
	RequireNetwork     *bool               `xml:"RunOnlyIfNetworkAvailable"`
	RequireIdle        *bool               `xml:"RunOnlyIfIdle"`
	AllowDemandStart   *bool               `xml:"AllowStartOnDemand"`
	Enabled            *bool               `xml:"Enabled"`
	Hidden             *bool               `xml:"Hidden"`
	ExecutionLimit     string              `xml:"ExecutionTimeLimit"`
	Restart            *windowsTaskRestart `xml:"RestartOnFailure,omitempty"`
}
type windowsTaskRestart struct {
	Interval string `xml:"Interval"`
	Count    int    `xml:"Count"`
}
type windowsTaskActions struct {
	Context string              `xml:"Context,attr"`
	Items   []windowsTaskAction `xml:",any"`
}
type windowsTaskAction struct {
	XMLName          xml.Name
	Command          string `xml:"Command"`
	Arguments        string `xml:"Arguments"`
	WorkingDirectory string `xml:"WorkingDirectory"`
}

// windowsTaskIdentity contains the native argument vector and resolved
// account security identifiers (SIDs), independent of XML display spelling.
type windowsTaskIdentity struct {
	Arguments    []string
	PrincipalSID string
	TriggerSID   string
}

// taskName identifies the scheduled task of an instance. It has no build tag,
// like the rest of this file, so the renderer coverage test can render the
// task's identity on every platform.
func taskName(spec LaunchSpec) string { return "leapmux-" + spec.Mode.String() + "-" + spec.InstanceID }

func taskDefinitionXML(spec LaunchSpec, arguments string) ([]byte, error) {
	definition := windowsTaskDefinition{
		Namespace: windowsTaskNamespace, Version: "1.2",
		Registration: windowsTaskRegistration{Description: windowsDefinitionDescription(spec)},
		Triggers:     windowsTaskTriggers{Items: []windowsTaskTrigger{{XMLName: xml.Name{Local: "LogonTrigger"}, Enabled: new(true), UserID: spec.AccountID}}},
		Principals:   windowsTaskPrincipals{Items: []windowsTaskPrincipal{{ID: "LeapMux", UserID: spec.AccountID, LogonType: "Password", RunLevel: new("LeastPrivilege")}}},
		Settings: windowsTaskSettings{
			MultipleInstances: "IgnoreNew", DisallowBattery: new(false), StopOnBattery: new(false), AllowHardTerminate: new(false),
			StartWhenAvailable: new(false), RequireNetwork: new(false), RequireIdle: new(false), AllowDemandStart: new(true),
			Enabled: new(true), Hidden: new(false), ExecutionLimit: "PT0S",
		},
		Actions: windowsTaskActions{Context: "LeapMux", Items: []windowsTaskAction{{XMLName: xml.Name{Local: "Exec"}, Command: spec.Executable, Arguments: arguments, WorkingDirectory: spec.WorkingDirectory}}},
	}
	return xml.Marshal(definition)
}

// parseWindowsTaskDefinition reads the decoded text returned by Task Scheduler.
func parseWindowsTaskDefinition(document string) (definition windowsTaskDefinition, err error) {
	decoder := xml.NewDecoder(strings.NewReader(document))
	decoder.CharsetReader = func(encoding string, input io.Reader) (io.Reader, error) {
		// go-ole already decoded the COM BSTR into this Go string. Transcoding its
		// UTF-8 bytes as UTF-16 would corrupt the task's Unicode paths.
		if strings.EqualFold(encoding, "UTF-16") {
			return input, nil
		}
		return nil, fmt.Errorf("the Windows task XML declares an unsupported encoding %q", encoding)
	}
	err = decoder.Decode(&definition)
	return definition, err
}

func taskSetting[T any](value *T, defaultValue T) T {
	if value == nil {
		return defaultValue
	}
	return *value
}

func sameWindowsPath(left, right string) bool {
	return strings.EqualFold(path.Clean(strings.ReplaceAll(left, `\`, "/")), path.Clean(strings.ReplaceAll(right, `\`, "/")))
}

func (definition windowsTaskDefinition) matches(spec LaunchSpec, identity windowsTaskIdentity) bool {
	namespace := definition.XMLName.Space
	if namespace == "" {
		namespace = definition.Namespace
	}
	if definition.XMLName.Local != "Task" || namespace != windowsTaskNamespace || definition.Registration.Description != windowsDefinitionDescription(spec) {
		return false
	}
	if len(definition.Triggers.Items) != 1 || len(definition.Principals.Items) != 1 || len(definition.Actions.Items) != 1 {
		return false
	}
	trigger, principal, action := definition.Triggers.Items[0], definition.Principals.Items[0], definition.Actions.Items[0]
	if trigger.XMLName.Local != "LogonTrigger" || !taskSetting(trigger.Enabled, true) || identity.TriggerSID != spec.AccountID ||
		trigger.StartBoundary != "" || trigger.EndBoundary != "" || trigger.Delay != "" && trigger.Delay != "PT0S" {
		return false
	}
	if identity.PrincipalSID != spec.AccountID || principal.LogonType != "Password" || taskSetting(principal.RunLevel, "LeastPrivilege") != "LeastPrivilege" {
		return false
	}
	if definition.Actions.Context != "" && definition.Actions.Context != principal.ID {
		return false
	}
	if action.XMLName.Local != "Exec" || !sameWindowsPath(action.Command, spec.Executable) || !sameWindowsPath(action.WorkingDirectory, spec.WorkingDirectory) ||
		!slices.Equal(identity.Arguments, spec.RunArguments()) {
		return false
	}
	settings := definition.Settings
	return settings.MultipleInstances == "IgnoreNew" && !taskSetting(settings.DisallowBattery, true) && !taskSetting(settings.StopOnBattery, true) &&
		!taskSetting(settings.AllowHardTerminate, true) && !taskSetting(settings.StartWhenAvailable, false) && !taskSetting(settings.RequireNetwork, false) &&
		!taskSetting(settings.RequireIdle, false) && taskSetting(settings.AllowDemandStart, true) && taskSetting(settings.Enabled, true) &&
		!taskSetting(settings.Hidden, false) && settings.ExecutionLimit == "PT0S" && settings.Restart == nil
}
