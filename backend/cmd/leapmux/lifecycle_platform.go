package main

import (
	"os"
	"os/user"

	"github.com/leapmux/leapmux/internal/servicelaunch"
)

// platformManagerFor selects this platform's native adapter. A platform
// without one gets the UnsupportedManager, whose refusals name the
// foreground alternative.
func platformManagerFor() servicelaunch.Manager {
	return servicelaunch.NewPlatformManager()
}

// currentAccountName states the login name of this account.
func currentAccountName() string {
	if current, err := user.Current(); err == nil {
		return current.Username
	}
	return os.Getenv("USER")
}
