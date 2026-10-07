import { mkdirSync, writeFileSync } from 'node:fs'
import { join } from 'node:path'

/**
 * Disable Grok requests outside scripted turns and prevent access to remote endpoints.
 *
 * Grok's sandbox in xai-grok-test-support/src/sandbox.rs uses the same telemetry, feedback, and update switches.
 * Each of these native tasks sends a model request after a turn:
 * - Turn summary.
 * - Title refresh.
 * - Session recap.
 * - Prompt suggestions.
 * - Memory processing.
 *
 * Those requests contain no authored test turn.
 * The first session title cannot be disabled. The housekeeping rules in ./mockModelScenario answer it.
 */
const GROK_QUIET_ENV: Readonly<Record<string, string>> = {
  GROK_DISABLE_AUTOUPDATER: '1',
  GROK_TELEMETRY_ENABLED: 'false',
  GROK_TELEMETRY_MIXPANEL_ENABLED: 'false',
  GROK_TELEMETRY_TRACE_UPLOAD: 'false',
  GROK_TRACE_UPLOAD: 'false',
  GROK_FEEDBACK_ENABLED: 'false',
  GROK_INSTRUMENTATION: 'disabled',
  OTEL_SDK_DISABLED: 'true',
  GROK_TURN_SUMMARY: '0',
  GROK_TITLE_REFRESH: '0',
  GROK_SESSION_RECAP: '0',
  GROK_PROMPT_SUGGESTIONS: '0',
  GROK_MEMORY: '0',
  GROK_TWO_PASS_COMPACTION: '0',
  // The credential-less warm-up `GET /` that Grok sends to a model origin.
  GROK_SAMPLER_SHARED_CLIENT: '0',
  GROK_MANAGED_CONFIG: '0',
  GROK_CAMPAIGNS: '0',
  // Grok otherwise runs `$SHELL -l` to capture a login environment, which reads
  // the developer's own shell profile.
  GROK_LOGIN_ENV: '0',
  // A fixed id, so an empty `GROK_HOME` does not compute one per process.
  GROK_AGENT_ID: 'leapmux-e2e',
}

export interface GrokEnvironmentOptions {
  /** The run directory, which holds Grok's lock slots. */
  runDirectory: string
  /** The isolated HOME of the run. */
  homeDir: string
  /** The OpenAI-compatible base URL of the mock, which ends in `/v1`. */
  baseURL: string
  modelKey: string
  /** The model that Grok uses for each turn and for the session summary. */
  modelID: string
  /** A second model, so a spec can switch models. It offers no reasoning effort. */
  alternateModelID: string
  /**
   * A third model, which offers the effort ladder of the default model and starts at High, not Medium. A model switch
   * that keeps a level then differs from a switch that takes the default of the new model.
   */
  reasoningModelID: string
}

/** Point Grok Build at the mock through its own `config.toml`, and turn off every model call that no test scripts. */
export function createGrokEnvironment(options: GrokEnvironmentOptions): Record<string, string> {
  const grokHome = join(options.homeDir, '.grok')
  const lockSlotDir = join(options.runDirectory, 'grok-lock-slots')
  mkdirSync(grokHome, { recursive: true })
  mkdirSync(lockSlotDir, { recursive: true })
  writeFileSync(join(grokHome, 'config.toml'), grokConfig(options), { mode: 0o600 })
  return {
    GROK_HOME: grokHome,
    // Grok's leader lock keeps acquire slots under the system temporary
    // directory; this keeps them inside the run.
    GROK_FILE_LOCK_SLOT_DIR: lockSlotDir,
    ...GROK_QUIET_ENV,
  }
}

/** The `[[model."<id>".reasoning_efforts]]` tables of one model: low, medium, and high, with `defaultLevel` as the default. */
function grokReasoningEfforts(model: string, defaultLevel: 'low' | 'medium' | 'high'): string {
  return (['low', 'medium', 'high'] as const).map(level => `[[model."${model}".reasoning_efforts]]
id = "${level}"
value = "${level}"
label = "${level[0]!.toUpperCase()}${level.slice(1)}"
default = ${level === defaultLevel}
`).join('\n')
}

/**
 * Configure Grok Build with three private models.
 *
 * remote_fetch = false disables remote settings and catalog requests.
 * The fixture hides both built-in models, so LeapMux's catalog contains only the mock.
 * The session title uses the default model too.
 * Otherwise Grok selects an auxiliary model that the mock does not advertise.
 */
function grokConfig(options: GrokEnvironmentOptions): string {
  const model = options.modelID
  const alternate = options.alternateModelID
  const reasoning = options.reasoningModelID
  return `[cli]
auto_update = false

[features]
telemetry = false
remote_fetch = false

[models]
default = "${model}"
session_summary = "${model}"

[model."grok-4.6"]
hidden = true

[model."grok-4.5"]
hidden = true

[model."${model}"]
model = "${model}"
base_url = "${options.baseURL}"
name = "Grok E2E"
api_key = "${options.modelKey}"
api_backend = "chat_completions"
context_window = 128000
supports_reasoning_effort = true
reasoning_effort = "medium"

${grokReasoningEfforts(model, 'medium')}
[model."${alternate}"]
model = "${alternate}"
base_url = "${options.baseURL}"
name = "Grok E2E Alternate"
api_key = "${options.modelKey}"
api_backend = "chat_completions"
context_window = 128000

[model."${reasoning}"]
model = "${reasoning}"
base_url = "${options.baseURL}"
name = "Grok E2E Reasoning"
api_key = "${options.modelKey}"
api_backend = "chat_completions"
context_window = 128000
supports_reasoning_effort = true
reasoning_effort = "high"

${grokReasoningEfforts(reasoning, 'high')}`
}
