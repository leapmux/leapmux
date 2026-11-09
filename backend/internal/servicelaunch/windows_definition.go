package servicelaunch

// windowsDefinitionDescription renders the identity of the saved
// specification into the one free-form field that both Windows mechanisms
// keep: the scheduled task's RegistrationInfo description and the service's
// SCM description. The working directory is part of the text because a
// Windows service definition has no working-directory field. The
// description carries the value, so the service definition drifts when the
// working directory changes.
//
// The file has no build tag because the task definition renders and matches
// its XML on every platform, and its tests run everywhere.
func windowsDefinitionDescription(spec LaunchSpec) string {
	return "LeapMux " + spec.Mode.String() + " (" + spec.InstanceID + ", account " + spec.AccountID + ", working directory " + spec.WorkingDirectory + ")"
}
