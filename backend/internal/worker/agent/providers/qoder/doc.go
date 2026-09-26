// Package qoder drives Qoder CLI, Qoder AI's terminal coding agent, over its
// stream-json stdio protocol.
//
// Qoder is a rebranded Gemini CLI with a stream-json translation layer on top:
// the envelope resembles Claude Code's and the semantics differ. The package
// shares no code with providers/claude. The stream carries a first-class
// control_request / control_response channel for permissions, questions, plan
// mode, goals and interrupt, and it is the only surface with goals and
// steering.
//
// The worker runs one long-lived `qodercli` process per agent:
//
//	qodercli --config-dir <dir> -p --input-format stream-json --output-format stream-json
//
// Isolation is `--config-dir` plus QODER_SITE=GLOBAL and QODER_FORCE_FILE_STORAGE=1.
// See frontend/tests/e2e/helpers/mockAgentEnvironment.ts for the E2E recipe.
package qoder
