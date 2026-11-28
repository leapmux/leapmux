package providers

import (
	"go/ast"
	"go/token"
	"os"
	"slices"
	"sort"
	"testing"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/util/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// agenttestPath is the import path of the shared test support whose suites the
// provider packages run.
const agenttestPath = "github.com/leapmux/leapmux/internal/worker/agent/agenttest"

// providerDirs maps each provider to the directory, under this package, that
// holds its package.
var providerDirs = map[leapmuxv1.AgentProvider]string{
	leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE:      "claude",
	leapmuxv1.AgentProvider_AGENT_PROVIDER_CODEX:            "codex",
	leapmuxv1.AgentProvider_AGENT_PROVIDER_CURSOR:           "cursor",
	leapmuxv1.AgentProvider_AGENT_PROVIDER_GITHUB_COPILOT:   "copilot",
	leapmuxv1.AgentProvider_AGENT_PROVIDER_KILO:             "kilo",
	leapmuxv1.AgentProvider_AGENT_PROVIDER_OPENCODE:         "opencode",
	leapmuxv1.AgentProvider_AGENT_PROVIDER_GOOSE:            "goose",
	leapmuxv1.AgentProvider_AGENT_PROVIDER_PI:               "pi",
	leapmuxv1.AgentProvider_AGENT_PROVIDER_REASONIX:         "reasonix",
	leapmuxv1.AgentProvider_AGENT_PROVIDER_ZCODE:            "zcode",
	leapmuxv1.AgentProvider_AGENT_PROVIDER_CODEWHALE:        "codewhale",
	leapmuxv1.AgentProvider_AGENT_PROVIDER_KIMI_CODE:        "kimi",
	leapmuxv1.AgentProvider_AGENT_PROVIDER_MIMO_CODE:        "mimo",
	leapmuxv1.AgentProvider_AGENT_PROVIDER_QWEN_CODE:        "qwen",
	leapmuxv1.AgentProvider_AGENT_PROVIDER_OH_MY_PI:         "ohmypi",
	leapmuxv1.AgentProvider_AGENT_PROVIDER_GROK_BUILD:       "grok",
	leapmuxv1.AgentProvider_AGENT_PROVIDER_KIRO:             "kiro",
	leapmuxv1.AgentProvider_AGENT_PROVIDER_AMP:              "amp",
	leapmuxv1.AgentProvider_AGENT_PROVIDER_CLINE:            "cline",
	leapmuxv1.AgentProvider_AGENT_PROVIDER_CODEBUDDY:        "codebuddy",
	leapmuxv1.AgentProvider_AGENT_PROVIDER_QODER:            "qoder",
	leapmuxv1.AgentProvider_AGENT_PROVIDER_LETTA:            "letta",
	leapmuxv1.AgentProvider_AGENT_PROVIDER_DROID:            "droid",
	leapmuxv1.AgentProvider_AGENT_PROVIDER_JUNIE:            "junie",
	leapmuxv1.AgentProvider_AGENT_PROVIDER_DIRAC:            "dirac",
	leapmuxv1.AgentProvider_AGENT_PROVIDER_FAST_AGENT:       "fastagent",
	leapmuxv1.AgentProvider_AGENT_PROVIDER_COMMAND_CODE:     "commandcode",
	leapmuxv1.AgentProvider_AGENT_PROVIDER_DEEPSEEK_HARNESS: "deepseekharness",
	leapmuxv1.AgentProvider_AGENT_PROVIDER_MUSE_CODE:        "muse",
	leapmuxv1.AgentProvider_AGENT_PROVIDER_GEMINI_CLI:       "gemini",
	leapmuxv1.AgentProvider_AGENT_PROVIDER_ANTIGRAVITY_CLI:  "antigravity",
}

// requiredSuites states, for each package directory, the agenttest suites that
// the tests of that package must call. A suite pins one rule that every provider
// of its kind must keep, so a call that goes missing leaves the rule unchecked
// for that provider, and nothing else reports it.
//
// The ACP providers run the turn, busy-refusal, session-input and
// control-response suites once, over the shared base in package acp, because
// the base implements those rules for all twelve providers. OpenCode also runs the
// busy refusal over its own agent type.
//
// Each package that publishes a control request runs AssertPublishedControlKeys
// over each kind that it publishes, because the Worker refuses every answer to a
// request whose payload gives another key than its publication. Each ACP
// provider that runs AssertControlIdentitiesStaySeparate runs it beside that
// suite, over the same publications, and the acp package runs it once for the
// base. A package whose row lists no AssertPublishedControlKeys reads its keys
// with the method of the base that it embeds, which that base's package checks,
// so it may not state a ControlRequestKey of its own
// (TestAPackageWithItsOwnControlRequestKeyChecksItsKeys).
var requiredSuites = map[string][]string{
	"muse": {"AssertUnknownFramePersists", "RequireReadsSessionStore", "AssertTurnFrames", "AssertRisingTurnTokens", "AssertBusyRefusalRepublishesTheTurn", "AssertRejectsMissingAndReplacedSessions", "AssertPreservesTheResponseWithoutARequest", "AssertWithholdsTheResponseForAMalformedRequest", "AssertTokenResumeRule", "AssertChildCapabilities", "RunSupplementConformance", "AssertPublishedControlKeys"},

	"commandcode": {
		"AssertUnknownFramePersists", "RunRetryAttemptCase",
		"RequireReadsSessionStore", "AssertTurnFrames", "AssertRisingTurnTokens",
		"AssertBusyRefusalRepublishesTheTurn", "AssertRejectsMissingAndReplacedSessions",
		"AssertPreservesTheResponseWithoutARequest", "AssertWithholdsTheResponseForAMalformedRequest",
		"AssertTokenResumeRule", "AssertChildCapabilities",
	},
	"deepseekharness": {
		"AssertUnknownFramePersists",
		"RequireReadsSessionStore", "AssertTurnFrames", "AssertRisingTurnTokens",
		"AssertBusyRefusalRepublishesTheTurn", "AssertRejectsMissingAndReplacedSessions",
		"AssertPreservesTheResponseWithoutARequest", "AssertWithholdsTheResponseForAMalformedRequest",
		"AssertTokenResumeRule", "AssertChildCapabilities", "AssertPublishedControlKeys",
	},
	"gemini": {
		"RequireReadsSessionStore", "AssertTokenResumeRule", "AssertControlIdentitiesStaySeparate", "AssertChildCapabilities", "AssertPublishedControlKeys",
	},
	"amp": {
		"AssertUnknownFramePersists",
		"RequireReadsSessionStore", "AssertTurnFrames", "AssertRisingTurnTokens",
		"AssertBusyRefusalRepublishesTheTurn", "AssertRejectsMissingAndReplacedSessions",
		"AssertPreservesTheResponseWithoutARequest", "AssertWithholdsTheResponseForAMalformedRequest",
		"AssertTokenResumeRule", "AssertChildCapabilities", "AssertPublishedControlKeys",
	},
	"antigravity": {
		"AssertUnknownFramePersists", "RunRetryAttemptCase",
		"RequireReadsSessionStore", "AssertTurnFrames", "AssertRisingTurnTokens",
		"AssertBusyRefusalRepublishesTheTurn", "AssertRejectsMissingAndReplacedSessions",
		"AssertPreservesTheResponseWithoutARequest", "AssertWithholdsTheResponseForAMalformedRequest",
		"AssertTokenResumeRule", "AssertChildCapabilities", "AssertPublishedControlKeys",
	},
	"acp": {
		"AssertUnknownFramePersists",
		"AssertTurnFrames", "AssertRisingTurnTokens", "AssertBusyRefusalRepublishesTheTurn",
		"AssertRejectsMissingAndReplacedSessions",
		"AssertPreservesTheResponseWithoutARequest", "AssertWithholdsTheResponseForAMalformedRequest",
		"RunSupplementConformance", "AssertPublishedControlKeys",
	},
	"claude": {
		"AssertUnknownFramePersists",
		"RequireReadsSessionStore", "AssertTurnFrames", "AssertRisingTurnTokens",
		"AssertBusyRefusalRepublishesTheTurn", "AssertRejectsMissingAndReplacedSessions",
		"AssertPreservesTheResponseWithoutARequest", "AssertTokenResumeRule", "AssertChildCapabilities", "AssertPublishedControlKeys",
	},
	"cline": {"AssertUnknownFramePersists",
		"RequireReadsSessionStore", "AssertTurnFrames", "AssertRisingTurnTokens",
		"AssertBusyRefusalRepublishesTheTurn", "AssertRejectsMissingAndReplacedSessions",
		"AssertPreservesTheResponseWithoutARequest", "AssertWithholdsTheResponseForAMalformedRequest",
		"AssertTokenResumeRule", "AssertChildCapabilities", "AssertPublishedControlKeys",
	},
	"codebuddy": {
		"AssertUnknownFramePersists",
		"RequireReadsSessionStore", "AssertTurnFrames", "AssertRisingTurnTokens",
		"AssertBusyRefusalRepublishesTheTurn", "AssertRejectsMissingAndReplacedSessions",
		"AssertPreservesTheResponseWithoutARequest", "AssertWithholdsTheResponseForAMalformedRequest",
		"AssertTokenResumeRule", "AssertChildCapabilities", "AssertPublishedControlKeys",
	},
	"qoder": {"AssertUnknownFramePersists",
		"RequireReadsSessionStore", "AssertTurnFrames", "AssertRisingTurnTokens",
		"AssertBusyRefusalRepublishesTheTurn", "AssertRejectsMissingAndReplacedSessions",
		"AssertPreservesTheResponseWithoutARequest", "AssertWithholdsTheResponseForAMalformedRequest",
		"AssertTokenResumeRule", "AssertChildCapabilities", "AssertPublishedControlKeys",
	},
	"droid": {
		"AssertUnknownFramePersists", "RunRetryAttemptCase",
		"RequireReadsSessionStore", "AssertTurnFrames", "AssertRisingTurnTokens",
		"AssertBusyRefusalRepublishesTheTurn", "AssertRejectsMissingAndReplacedSessions",
		"AssertPreservesTheResponseWithoutARequest", "AssertWithholdsTheResponseForAMalformedRequest",
		"AssertTokenResumeRule", "AssertChildCapabilities", "AssertPublishedControlKeys",
	},
	"codewhale": {"AssertUnknownFramePersists", "RunRetryAttemptCase",
		"RequireReadsSessionStore", "AssertTurnFrames", "AssertRisingTurnTokens",
		"AssertBusyRefusalRepublishesTheTurn", "AssertRejectsMissingAndReplacedSessions",
		"AssertPreservesTheResponseWithoutARequest", "AssertWithholdsTheResponseForAMalformedRequest",
		"AssertTokenResumeRule", "AssertChildCapabilities", "AssertPublishedControlKeys",
	},
	"codex": {
		"AssertUnknownFramePersists", "RunRetryAttemptCase",
		"RequireReadsSessionStore", "AssertTurnFrames", "AssertRisingTurnTokens",
		"AssertBusyRefusalRepublishesTheTurn", "AssertRejectsMissingAndReplacedSessions",
		"AssertPreservesTheResponseWithoutARequest", "AssertWithholdsTheResponseForAMalformedRequest",
		"AssertTokenResumeRule", "AssertControlIdentitiesStaySeparate", "RunSupplementConformance", "AssertChildCapabilities", "AssertPublishedControlKeys",
	},
	"copilot": {
		"AssertUnknownFramePersists", "RunRetryAttemptCase",
		"RequireReadsSessionStore", "AssertTurnFrames", "AssertRisingTurnTokens",
		"AssertBusyRefusalRepublishesTheTurn", "AssertRejectsMissingAndReplacedSessions", "AssertChildCapabilities", "AssertPublishedControlKeys",
	},
	"cursor": {
		"RequireReadsSessionStore", "AssertTokenResumeRule", "AssertControlIdentitiesStaySeparate",
		"RequireToolStoreHandlesClosed", "AssertChildCapabilities", "AssertPublishedControlKeys",
	},
	"goose": {"AssertUnknownFramePersists", "RequireReadsSessionStore", "AssertChildCapabilities"},
	"grok": {"AssertUnknownFramePersists", "RunRetryAttemptCase",
		"RequireReadsSessionStore", "AssertTokenResumeRule", "AssertControlIdentitiesStaySeparate", "AssertChildCapabilities", "AssertPublishedControlKeys"},
	"kilo": {"RunRetryAttemptCase", "RequireReadsSessionStore", "AssertChildCapabilities"},
	"letta": {"AssertUnknownFramePersists", "RunRetryAttemptCase",
		"RequireReadsSessionStore", "AssertTurnFrames", "AssertRisingTurnTokens",
		"AssertBusyRefusalRepublishesTheTurn", "AssertRejectsMissingAndReplacedSessions",
		"AssertPreservesTheResponseWithoutARequest", "AssertWithholdsTheResponseForAMalformedRequest",
		"AssertTokenResumeRule", "AssertChildCapabilities", "AssertPublishedControlKeys",
	},
	"kiro":      {"AssertUnknownFramePersists", "RequireReadsSessionStore", "AssertTokenResumeRule", "AssertControlIdentitiesStaySeparate", "AssertChildCapabilities", "AssertPublishedControlKeys"},
	"dirac":     {"RequireReadsSessionStore", "AssertChildCapabilities"},
	"fastagent": {"RequireReadsSessionStore", "AssertChildCapabilities"},
	"junie":     {"RequireReadsSessionStore", "AssertChildCapabilities"},
	"kimi": {"AssertUnknownFramePersists", "RunRetryAttemptCase",
		"RequireReadsSessionStore", "AssertTurnFrames", "AssertRisingTurnTokens",
		"AssertBusyRefusalRepublishesTheTurn", "AssertRejectsMissingAndReplacedSessions",
		"AssertPreservesTheResponseWithoutARequest", "AssertWithholdsTheResponseForAMalformedRequest",
		"AssertTokenResumeRule", "AssertChildCapabilities", "AssertPublishedControlKeys",
	},
	"opencode": {
		"RunRetryAttemptCase",
		"RequireReadsSessionStore", "AssertBusyRefusalRepublishesTheTurn", "AssertTokenResumeRule",
		"AssertControlIdentitiesStaySeparate", "AssertChildCapabilities", "AssertPublishedControlKeys",
	},
	"pi": {
		"AssertUnknownFramePersists",
		"RequireReadsSessionStore", "AssertTurnFrames", "AssertRisingTurnTokens",
		"AssertBusyRefusalRepublishesTheTurn", "AssertRejectsMissingAndReplacedSessions",
		"AssertPreservesTheResponseWithoutARequest", "AssertWithholdsTheResponseForAMalformedRequest", "AssertChildCapabilities", "AssertPublishedControlKeys",
	},
	"ohmypi": {
		"AssertUnknownFramePersists",
		"RequireReadsSessionStore", "AssertTurnFrames", "AssertRisingTurnTokens",
		"AssertBusyRefusalRepublishesTheTurn", "AssertRejectsMissingAndReplacedSessions",
		"AssertPreservesTheResponseWithoutARequest", "AssertWithholdsTheResponseForAMalformedRequest", "AssertChildCapabilities", "AssertPublishedControlKeys",
	},
	"qwen": {"AssertUnknownFramePersists", "RunRetryAttemptCase",
		"RequireReadsSessionStore", "AssertTokenResumeRule", "AssertControlIdentitiesStaySeparate", "AssertChildCapabilities", "AssertPublishedControlKeys"},
	"reasonix": {"RequireReadsSessionStore", "AssertControlIdentitiesStaySeparate", "AssertChildCapabilities", "AssertPublishedControlKeys"},
	"zcode": {
		"AssertUnknownFramePersists", "RunRetryAttemptCase",
		"RequireReadsSessionStore", "AssertTurnFrames", "AssertRisingTurnTokens",
		"AssertBusyRefusalRepublishesTheTurn", "AssertRejectsMissingAndReplacedSessions",
		"AssertTokenResumeRule", "RequireToolStoreHandlesClosed", "AssertChildCapabilities", "AssertPublishedControlKeys",
	},
	"mimo": {"AssertUnknownFramePersists", "RunRetryAttemptCase",
		"RequireReadsSessionStore", "AssertTurnFrames", "AssertRisingTurnTokens",
		"AssertBusyRefusalRepublishesTheTurn", "AssertRejectsMissingAndReplacedSessions",
		"AssertPreservesTheResponseWithoutARequest", "AssertWithholdsTheResponseForAMalformedRequest",
		"AssertTokenResumeRule", "AssertChildCapabilities", "AssertPublishedControlKeys",
	},
}

func assertEveryRegisteredProviderRunsSuite(t *testing.T, suite, requirement string) {
	t.Helper()
	providers := Registry().Providers()
	require.NotEmpty(t, providers, "the sweep proves nothing against an empty registry")
	for _, id := range providers {
		t.Run(id.String(), func(t *testing.T) {
			t.Parallel()
			dir, ok := providerDirs[id]
			require.Truef(t, ok, "%v has no package directory in providerDirs", id)
			assert.Truef(t, suitesCalledIn(t, dir)[suite], requirement, dir)
		})
	}
}

// TestEveryRegisteredProviderReadsItsSessionStore sweeps the registry rather
// than a list of providers, so a provider added after this was written fails
// until its package states what its session store is. The package states that
// with a test that runs agenttest.RequireReadsSessionStore over a fixture of the
// store, or with a row here that gives the reason it cannot.
func TestEveryRegisteredProviderReadsItsSessionStore(t *testing.T) {
	t.Parallel()
	assertEveryRegisteredProviderRunsSuite(t, "RequireReadsSessionStore",
		"the tests of %s must run agenttest.RequireReadsSessionStore over a fixture of its session store")
}

// TestEveryRegisteredProviderReadsItsReplyText sweeps the registry, so a
// provider added after this was written fails until its package replays its
// cases of testdata/reply_text_conformance.json through
// agenttest.RunReplyTextConformance. The suite itself fails a provider with no
// case, so the sweep and the corpus together cover every provider.
func TestEveryRegisteredProviderReadsItsReplyText(t *testing.T) {
	t.Parallel()
	assertEveryRegisteredProviderRunsSuite(t, "RunReplyTextConformance",
		"the tests of %s must run agenttest.RunReplyTextConformance over its cases of testdata/reply_text_conformance.json")
}

// TestEveryProviderPackageRunsItsConformanceSuites checks each row of
// requiredSuites against the tests of its package, and checks that each
// registered provider has a row.
func TestEveryProviderPackageRunsItsConformanceSuites(t *testing.T) {
	t.Parallel()

	for _, id := range Registry().Providers() {
		dir, ok := providerDirs[id]
		require.Truef(t, ok, "%v has no package directory in providerDirs", id)
		_, ok = requiredSuites[dir]
		assert.Truef(t, ok, "%v has no row in requiredSuites; state the suites that its package runs", id)
	}
	dirs := make([]string, 0, len(requiredSuites))
	for dir := range requiredSuites {
		dirs = append(dirs, dir)
	}
	sort.Strings(dirs)
	for _, dir := range dirs {
		t.Run(dir, func(t *testing.T) {
			t.Parallel()
			called := suitesCalledIn(t, dir)
			for _, suite := range requiredSuites[dir] {
				assert.Truef(t, called[suite], "the tests of %s must call agenttest.%s", dir, suite)
			}
		})
	}
}

// TestAPackageWithItsOwnControlRequestKeyChecksItsKeys keeps the exemption of
// a row with no AssertPublishedControlKeys true: such a package inherits its
// ControlRequestKey from a base whose own package checks it. A method of the
// package's own would derive keys that no test compares with the package's
// publications, and the Worker refuses every answer to a request whose key
// disagrees.
func TestAPackageWithItsOwnControlRequestKeyChecksItsKeys(t *testing.T) {
	t.Parallel()

	exempt := 0
	for dir, suites := range requiredSuites {
		if slices.Contains(suites, "AssertPublishedControlKeys") {
			continue
		}
		exempt++
		testutil.ForEachPackageSourceFile(t, dir, func(fset *token.FileSet, file *ast.File) {
			for _, decl := range file.Decls {
				if fn, ok := decl.(*ast.FuncDecl); ok && fn.Recv != nil && fn.Name.Name == "ControlRequestKey" {
					assert.Failf(t, "a key that no test checks",
						"%s states its own ControlRequestKey at %s, so its tests must call agenttest.AssertPublishedControlKeys and its row in requiredSuites must list it",
						dir, fset.Position(fn.Pos()))
				}
			}
		})
	}
	require.NotZero(t, exempt, "no row is exempt, so the sweep checks nothing; delete it")
}

// TestRequiredSuitesExistInAgenttest keeps requiredSuites honest: a suite that
// agenttest renamed or removed would otherwise read as missing from every row,
// and a spelling mistake in a row would never match.
func TestRequiredSuitesExistInAgenttest(t *testing.T) {
	t.Parallel()

	declared := map[string]bool{}
	testutil.ForEachPackageSourceFile(t, "../agenttest", func(_ *token.FileSet, file *ast.File) {
		for _, decl := range file.Decls {
			if fn, ok := decl.(*ast.FuncDecl); ok && fn.Recv == nil {
				declared[fn.Name.Name] = true
			}
		}
	})
	for dir, suites := range requiredSuites {
		_, err := os.Stat(dir)
		require.NoErrorf(t, err, "requiredSuites has a row for %s, which is not a directory here", dir)
		for _, suite := range suites {
			assert.Truef(t, declared[suite], "requiredSuites[%s] lists %s, which agenttest does not declare", dir, suite)
		}
	}
}

// suitesCalledIn returns the name of each agenttest function that a test file of
// the package in dir calls.
func suitesCalledIn(t *testing.T, dir string) map[string]bool {
	t.Helper()
	called := map[string]bool{}
	testutil.ForEachPackageTestFile(t, dir, func(_ *token.FileSet, file *ast.File) {
		alias, ok := testutil.ImportedAs(file, agenttestPath)
		if !ok {
			return
		}
		ast.Inspect(file, func(node ast.Node) bool {
			call, isCall := node.(*ast.CallExpr)
			if !isCall {
				return true
			}
			sel, isSel := call.Fun.(*ast.SelectorExpr)
			if !isSel {
				return true
			}
			if x, isIdent := sel.X.(*ast.Ident); isIdent && x.Name == alias {
				called[sel.Sel.Name] = true
			}
			return true
		})
	})
	return called
}
