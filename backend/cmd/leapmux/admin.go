package main

import (
	"fmt"
	"os"
	"strconv"

	"github.com/leapmux/leapmux/internal/hub/keystore"
)

func runAdmin(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: leapmux admin <command> [flags]\n\nCommands:\n  rotate-encryption-key    Generate and add a new encryption key version\n  remove-encryption-key    Remove an old encryption key version\n  reencrypt-secrets        Re-encrypt all secrets with the active key")
	}

	switch args[0] {
	case "rotate-encryption-key":
		return runRotateEncryptionKey(args[1:])
	case "remove-encryption-key":
		return runRemoveEncryptionKey(args[1:])
	case "reencrypt-secrets":
		return runReencryptSecrets(args[1:])
	default:
		return fmt.Errorf("unknown admin command: %s", args[0])
	}
}

func runRotateEncryptionKey(args []string) error {
	path := encryptionKeyPath(args)

	// Ensure the key ring file exists.
	if _, err := os.Stat(path); os.IsNotExist(err) {
		return fmt.Errorf("encryption key file not found at %s\nRun the hub once to auto-generate it, or specify --data-dir", path)
	}

	newVersion, err := keystore.RotateKey(path)
	if err != nil {
		return err
	}

	fmt.Printf("Added encryption key version %d.\n", newVersion)
	fmt.Printf("Restart the hub, then run: leapmux admin reencrypt-secrets\n")
	return nil
}

func runRemoveEncryptionKey(args []string) error {
	var version int
	path := defaultEncryptionKeyPath()

	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--version":
			if i+1 >= len(args) {
				return fmt.Errorf("--version requires a value")
			}
			i++
			v, err := strconv.Atoi(args[i])
			if err != nil || v < 1 || v > 255 {
				return fmt.Errorf("invalid version: %s (must be 1-255)", args[i])
			}
			version = v
		case "--data-dir":
			if i+1 >= len(args) {
				return fmt.Errorf("--data-dir requires a value")
			}
			i++
			path = args[i] + "/encryption.key"
		default:
			return fmt.Errorf("unknown flag: %s", args[i])
		}
	}

	if version == 0 {
		return fmt.Errorf("--version is required")
	}

	// TODO: Check DB for rows referencing this version before removing.
	// This will be implemented in Phase 3 when OAuth tables exist.

	if err := keystore.RemoveKey(path, byte(version)); err != nil {
		return err
	}

	fmt.Printf("Removed encryption key version %d.\n", version)
	fmt.Printf("Restart the hub to apply.\n")
	return nil
}

func runReencryptSecrets(_ []string) error {
	// TODO: Implement in Phase 3 when OAuth tables and encrypted columns exist.
	fmt.Println("No encrypted secrets to re-encrypt yet. OAuth tables are not yet implemented.")
	return nil
}

func encryptionKeyPath(args []string) string {
	for i := 0; i < len(args); i++ {
		if args[i] == "--data-dir" && i+1 < len(args) {
			return args[i+1] + "/encryption.key"
		}
	}
	return defaultEncryptionKeyPath()
}

func defaultEncryptionKeyPath() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return "encryption.key"
	}
	return home + "/.config/leapmux/hub/encryption.key"
}
