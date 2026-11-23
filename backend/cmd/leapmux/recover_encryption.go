package main

import (
	"context"
	"flag"
	"fmt"
	"math"
	"slices"
	"strings"

	"github.com/leapmux/leapmux/internal/hub/config"
	"github.com/leapmux/leapmux/internal/hub/keystore"
	"github.com/leapmux/leapmux/internal/hub/store"
)

func runRotateEncryptionKey(cmd cmdCtx, args []string) error {
	return withRecoverConfig(cmd, args, nil, func(cfg *config.Config) error {
		path := cfg.EncryptionKeyFilePath()

		if _, err := loadExistingKeystore(path); err != nil {
			return err
		}

		newVersion, err := keystore.RotateKey(path)
		if err != nil {
			return err
		}

		fmt.Printf("Added encryption key version %d.\n", newVersion)
		fmt.Printf("Restart the hub, then run: leapmux recover encryption-key reencrypt\n")
		return nil
	})
}

func runRemoveEncryptionKey(cmd cmdCtx, args []string) error {
	var version *uint
	return withRecoverExistingStore(cmd, args, func(fs *flag.FlagSet) {
		version = fs.Uint("version", 0, "key version to remove")
	}, func(ctx context.Context, cfg *config.Config, st store.Store, ks *keystore.Keystore) error {
		if *version == 0 || *version > math.MaxUint32 {
			return fmt.Errorf("--version must be between 1 and %d", uint64(math.MaxUint32))
		}

		v := uint32(*version)
		if v == ks.ActiveVersion() {
			return fmt.Errorf("cannot remove active key version %d", v)
		}
		if !slices.Contains(ks.Versions(), v) {
			return fmt.Errorf("keystore: key version %d not in ring", v)
		}

		// Guard: refuse to remove a version that still encrypts persistent
		// data. Removing it would make those rows permanently unreadable
		// (decryption fails with "unknown key version"). The operator must run
		// reencrypt first to migrate them onto the active key.
		counts, err := st.KeyMaintenance().CountKeyVersionRefs(ctx, v)
		if err != nil {
			return err
		}
		if len(counts) > 0 {
			refs := make([]string, 0, len(counts))
			for _, c := range counts {
				if c.Count > 0 {
					refs = append(refs, fmt.Sprintf("%d %s", c.Count, c.Label))
				}
			}
			if len(refs) > 0 {
				return fmt.Errorf("encryption key version %d still encrypts %s; run 'leapmux recover encryption-key reencrypt' first (after restarting the hub on the rotated key)", v, strings.Join(refs, ", "))
			}
		}

		if err := keystore.RemoveKey(cfg.EncryptionKeyFilePath(), v); err != nil {
			return err
		}

		fmt.Printf("Removed encryption key version %d.\n", v)
		fmt.Printf("Restart the hub to apply.\n")
		return nil
	})
}

// pepperRotationEffects is shared by confirmation and completion output.
const pepperRotationEffects = "- Signs out every session.\n" +
	"- Invalidates every Worker auth token.\n" +
	"- Invalidates API and delegation tokens and confidential app secrets.\n" +
	"- Invalidates live registration keys, pending device codes, and pending OAuth authorization codes.\n"

// runRotatePepper replaces the HMAC pepper for the credential classes listed
// in pepperRotationEffects.
// The resulting invalidations require --yes. The encryption key ring stays intact.
func runRotatePepper(cmd cmdCtx, args []string) error {
	var yes bool
	return withRecoverConfig(cmd, args, func(fs *flag.FlagSet) {
		fs.BoolVar(&yes, "yes", false, "confirm these effects after the Hub restarts:\n"+pepperRotationEffects)
	}, func(cfg *config.Config) error {
		path := cfg.EncryptionKeyFilePath()
		if _, err := loadExistingKeystore(path); err != nil {
			return err
		}
		if !yes {
			return fmt.Errorf("rotating the token pepper causes these effects after the Hub restarts:\n%sRe-run with --yes to proceed", pepperRotationEffects)
		}
		if err := keystore.RegeneratePepper(path); err != nil {
			return err
		}
		fmt.Println("Regenerated the token pepper.")
		fmt.Print(pepperRotationEffects)
		fmt.Println("Restart the Hub to apply the new pepper.")
		fmt.Println("Solo mode and the desktop app reissue their own Worker's token.")
		fmt.Println("For each standalone Worker:")
		fmt.Println("1. Remove the Worker in the Hub's Worker list.")
		fmt.Println("2. Stop the Worker.")
		fmt.Println("3. Delete its state.json.")
		fmt.Println("4. Register it with a new registration key. It receives a new Worker ID.")
		fmt.Println("Re-issue API tokens with: leapmux control admin api-token issue --hub <url>")
		return nil
	})
}

func runReencryptSecrets(cmd cmdCtx, args []string) error {
	return withRecoverExistingStore(cmd, args, nil, func(ctx context.Context, _ *config.Config, st store.Store, _ *keystore.Keystore) error {
		res, err := st.KeyMaintenance().ReencryptAll(ctx)
		if err != nil {
			return err
		}
		fmt.Printf("Re-encrypted %d encrypted rows to key version %d.\n", res.RowsUpdated, res.ActiveVersion)
		return nil
	})
}
