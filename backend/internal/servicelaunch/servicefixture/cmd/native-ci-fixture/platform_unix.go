//go:build !windows

package main

import "os/user"

func prepareCredential(*user.User, string) (string, error) { return "", nil }
func cleanupCredential(string) error                       { return nil }
