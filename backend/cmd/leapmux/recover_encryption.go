package main

import (
	"fmt"
	"math"
	"slices"
	"strconv"
	"strings"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"

	"github.com/leapmux/leapmux/internal/cli"
	"github.com/leapmux/leapmux/internal/hub/config"
	"github.com/leapmux/leapmux/internal/hub/keystore"
	"github.com/leapmux/leapmux/internal/hub/store"
)

func newEncryptionKeyRotateCommand() *cobra.Command {
	return recoverConfigLeaf(recoverSpec{Leaf: cli.Leaf{
		Use:     "rotate",
		Short:   "Generate and add a new encryption key version",
		Mutates: true,
		Examples: []cli.Example{
			{Command: "leapmux recover encryption-key rotate"},
		},
	}}, func(inv *cli.Invocation, cfg *config.Config) error {
		path := cfg.EncryptionKeyFilePath()

		if _, err := loadExistingKeystore(path); err != nil {
			return err
		}

		newVersion, err := keystore.RotateKey(path)
		if err != nil {
			return err
		}

		_, err = fmt.Fprintf(inv.Printer.Out, "Added encryption key version %d.\nRestart the hub, then run: leapmux recover encryption-key reencrypt\n", newVersion)
		return err
	})
}

// keyVersionHelp describes the <version> argument of `encryption-key remove`.
var keyVersionHelp = fmt.Sprintf("The key version to remove, from 1 to %d. The active version cannot be removed.", uint64(math.MaxUint32))

func newEncryptionKeyRemoveCommand() *cobra.Command {
	var v uint32
	return recoverExistingStoreLeaf(recoverSpec{
		Leaf: cli.Leaf{
			Use:     "remove <version>",
			Aliases: []string{"rm"},
			Short:   "Remove an old encryption key version",
			Mutates: true,
			Args:    map[string]string{"version": keyVersionHelp},
			Examples: []cli.Example{
				{Comment: "Remove version 1 after reencrypt moved every encrypted row to a later version.", Command: "leapmux recover encryption-key remove 1"},
			},
		},
		BeforeOpen: func(inv *cli.Invocation) (err error) {
			v, err = parseKeyVersion(inv.Arg("version"))
			return err
		},
	}, func(inv *cli.Invocation, cfg *config.Config, st store.Store, ks *keystore.Keystore) error {
		ctx := inv.Context
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

		_, err = fmt.Fprintf(inv.Printer.Out, "Removed encryption key version %d.\nRestart the hub to apply.\n", v)
		return err
	})
}

// parseKeyVersion reads the <version> argument of `encryption-key remove`: a
// key version from 1 to the largest uint32.
func parseKeyVersion(raw string) (uint32, error) {
	v, err := strconv.ParseUint(strings.TrimSpace(raw), 10, 32)
	if err != nil || v == 0 {
		return 0, cli.Usagef("<version> %q is not a key version", raw).
			WithHint("give a whole number from 1 to %d", uint64(math.MaxUint32))
	}
	return uint32(v), nil
}

// pepperRotationEffects is shared by confirmation and completion output.
const pepperRotationEffects = "- Signs out every session.\n" +
	"- Invalidates every Worker auth token.\n" +
	"- Invalidates API and delegation tokens and confidential app secrets.\n" +
	"- Invalidates live registration keys, pending device codes, and pending OAuth authorization codes.\n"

// newEncryptionKeyRotatePepperCommand builds `leapmux recover encryption-key
// rotate-pepper`. It replaces the HMAC pepper for the credential classes
// listed in pepperRotationEffects. The resulting invalidations require --yes.
// The encryption key ring stays intact.
func newEncryptionKeyRotatePepperCommand() *cobra.Command {
	var yes bool
	return recoverConfigLeaf(recoverSpec{Leaf: cli.Leaf{
		Use:     "rotate-pepper",
		Short:   "Rotate the pepper; sign out sessions and invalidate Worker/API/delegation tokens, app secrets, registration keys, device codes, and authorization codes",
		Long:    "Rotate the token pepper. After the Hub restarts, the rotation has these effects:\n\n" + pepperRotationEffects + "\nThe encryption key ring stays unchanged.",
		Mutates: true,
		Examples: []cli.Example{
			{Command: "leapmux recover encryption-key rotate-pepper --yes"},
		},
		Flags: func(fs *pflag.FlagSet) {
			fs.BoolVar(&yes, "yes", false, "confirm the effects of the rotation, which the command help lists")
		},
	}}, func(inv *cli.Invocation, cfg *config.Config) error {
		path := cfg.EncryptionKeyFilePath()
		if _, err := loadExistingKeystore(path); err != nil {
			return err
		}
		if !yes {
			return cli.Usagef("rotating the token pepper requires --yes, because it has these effects after the Hub restarts:\n%s", strings.TrimSuffix(pepperRotationEffects, "\n")).
				WithHint("run the command again with --yes to proceed")
		}
		if err := keystore.RegeneratePepper(path); err != nil {
			return err
		}
		_, err := fmt.Fprint(inv.Printer.Out, "Regenerated the token pepper.\n"+
			pepperRotationEffects+
			"Restart the Hub to apply the new pepper.\n"+
			"Solo mode and the desktop app reissue their own Worker's token.\n"+
			"For each standalone Worker:\n"+
			"1. Remove the Worker in the Hub's Worker list.\n"+
			"2. Stop the Worker.\n"+
			"3. Delete its state.json.\n"+
			"4. Register it with a new registration key. It receives a new Worker ID.\n"+
			"Re-issue API tokens with: leapmux admin api-token issue <user> --installation-name <name> --hub <url>\n")
		return err
	})
}

func newEncryptionKeyReencryptCommand() *cobra.Command {
	return recoverExistingStoreLeaf(recoverSpec{Leaf: cli.Leaf{
		Use:     "reencrypt",
		Short:   "Re-encrypt all encrypted rows with the active key",
		Mutates: true,
		Examples: []cli.Example{
			{Comment: "Run it after rotate and a restart of the Hub.", Command: "leapmux recover encryption-key reencrypt"},
		},
	}}, func(inv *cli.Invocation, _ *config.Config, st store.Store, _ *keystore.Keystore) error {
		res, err := st.KeyMaintenance().ReencryptAll(inv.Context)
		if err != nil {
			return err
		}
		_, err = fmt.Fprintf(inv.Printer.Out, "Re-encrypted %d encrypted rows to key version %d.\n", res.RowsUpdated, res.ActiveVersion)
		return err
	})
}
