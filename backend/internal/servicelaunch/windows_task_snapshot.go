package servicelaunch

import "fmt"

type windowsTaskSnapshot struct {
	Access     windowsTaskAccess
	Definition windowsTaskDefinition
	Identity   windowsTaskIdentity
	State      int32
	Enabled    bool
	Instances  int32
	LastResult uint32
}

func (snapshot windowsTaskSnapshot) active() bool {
	return snapshot.Instances > 0 || snapshot.State == 2 || snapshot.State == 4
}
func (snapshot windowsTaskSnapshot) matches(spec LaunchSpec) bool {
	return snapshot.Enabled && snapshot.Definition.matches(spec, snapshot.Identity) && snapshot.Access.matches(spec.AccountID)
}

func startTaskSnapshot(snapshot windowsTaskSnapshot, spec LaunchSpec, run func() error) error {
	if !snapshot.matches(spec) {
		return fmt.Errorf("repair the scheduled task with enable before starting")
	}
	if snapshot.active() {
		return fmt.Errorf("the scheduled task is already running or starting")
	}
	return run()
}
