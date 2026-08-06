package memlimit

// stubProbes installs machine probes for one test and returns the restore
// func. Kept here rather than inline so every caller restores the same way --
// a test that left a stub installed would silently redefine the machine for
// every test that ran after it.
func stubProbes(cgroup, physical func() (int64, error)) func() {
	prevCgroup, prevPhysical := cgroupProbe, physicalProbe
	cgroupProbe, physicalProbe = cgroup, physical
	return func() { cgroupProbe, physicalProbe = prevCgroup, prevPhysical }
}
