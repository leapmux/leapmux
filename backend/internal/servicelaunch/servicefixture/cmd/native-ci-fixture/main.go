// native-ci-fixture prepares credentials only on disposable GitHub-hosted runners.
package main

import (
	"errors"
	"fmt"
	"log"
	"os"
	"os/user"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/leapmux/leapmux/internal/servicelaunch/servicefixture"
	"github.com/leapmux/leapmux/internal/util/atomicfile"
	"go.yaml.in/yaml/v3"
)

var runNumber = regexp.MustCompile(`^[1-9][0-9]*$`)
var repository = regexp.MustCompile(`^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$`)

func validateInvocation(env func(string) string, args []string) (string, error) {
	if env("GITHUB_ACTIONS") != "true" || env("RUNNER_ENVIRONMENT") != "github-hosted" || !runNumber.MatchString(env("GITHUB_RUN_ID")) || !runNumber.MatchString(env("GITHUB_RUN_ATTEMPT")) || !repository.MatchString(env("GITHUB_REPOSITORY")) {
		return "", errors.New("native fixture requires a GitHub-hosted disposable CI runner")
	}
	root := env("RUNNER_TEMP")
	if !filepath.IsAbs(root) {
		return "", errors.New("runner temporary directory must be absolute")
	}
	if len(args) == 1 && args[0] == "cleanup" {
		return credentialReference(env), nil
	}
	if len(args) != 2 || args[0] != "prepare" || !filepath.IsAbs(args[1]) {
		return "", errors.New("fixture preparation requires an absolute output path")
	}
	// Resolve both directories so existing symlinks cannot bypass the containment check.
	root, err := filepath.EvalSymlinks(root)
	if err != nil {
		return "", fmt.Errorf("resolve runner temporary directory: %w", err)
	}
	parent, err := filepath.EvalSymlinks(filepath.Dir(args[1]))
	if err != nil {
		return "", fmt.Errorf("resolve fixture directory: %w", err)
	}
	relative, err := filepath.Rel(root, parent)
	if err != nil || relative == ".." || filepath.IsAbs(relative) || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return "", errors.New("fixture output must remain inside the runner temporary directory")
	}
	return credentialReference(env), nil
}

func credentialReference(env func(string) string) string {
	return "LeapMux/CI/" + env("GITHUB_REPOSITORY") + "/" + env("GITHUB_RUN_ID") + "/" + env("GITHUB_RUN_ATTEMPT")
}

func run(env func(string) string, args []string) error {
	reference, err := validateInvocation(env, args)
	if err != nil {
		return err
	}
	if args[0] == "cleanup" {
		return cleanupCredential(reference)
	}
	account, err := user.Current()
	if err != nil {
		return err
	}
	credential, err := prepareCredential(account, reference)
	if err != nil {
		return err
	}
	fixture := servicefixture.Account{AccountID: account.Uid, AccountName: account.Username, DisposableEnvironment: true}
	if credential != "" {
		fixture.CredentialReference = &credential
	}
	data, err := yaml.Marshal(fixture)
	if err == nil {
		err = atomicfile.WriteFileDurable(args[1], data, 0600)
	}
	if err != nil {
		return errors.Join(fmt.Errorf("write private CI account fixture: %w", err), cleanupCredential(reference))
	}
	return nil
}

func main() {
	if err := run(os.Getenv, os.Args[1:]); err != nil {
		log.Fatal(err)
	}
}
