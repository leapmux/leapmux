import { mkdirSync, writeFileSync } from 'node:fs'
import { join } from 'node:path'

/** Reasonix qualifies default_model with its provider identifier. */
const REASONIX_PROVIDER_ID = 'deepseek'

export interface ReasonixEnvironmentOptions {
  /** The isolated HOME of the run. */
  homeDir: string
  /** The OpenAI-compatible base URL of the mock, which ends in `/v1`. */
  baseURL: string
  modelKey: string
  /** The model of the default provider, which also accepts image input. */
  modelID: string
  /** The second provider, so a spec can switch to a model of another provider. */
  alternateProviderID: string
  alternateModelID: string
}

/** Point Reasonix at the mock through two providers of its own `config.toml`, and bind its key variable. */
export function createReasonixEnvironment(options: ReasonixEnvironmentOptions): Record<string, string> {
  const reasonixHome = join(options.homeDir, '.reasonix')
  mkdirSync(reasonixHome, { recursive: true })
  writeFileSync(join(reasonixHome, 'config.toml'), reasonixConfig(options), { mode: 0o600 })
  writeFileSync(join(reasonixHome, '.env'), reasonixCredentials(options.modelKey), { mode: 0o600 })
  return { REASONIX_HOME: reasonixHome }
}

/**
 * Reasonix's credential file, which binds the variable of `api_key_env` to the mock key.
 *
 * Reasonix 1.38 reads that variable only from `$REASONIX_HOME/.env` and never from the
 * process environment (internal/config/config.go ProviderEntry.APIKey). A loopback
 * base_url needs no key, so without this file each request reaches the mock with no
 * credential.
 */
function reasonixCredentials(modelKey: string): string {
  return `LEAPMUX_E2E_MODEL_API_KEY=${modelKey}\n`
}

function reasonixConfig(options: ReasonixEnvironmentOptions): string {
  return `default_model = "${REASONIX_PROVIDER_ID}/${options.modelID}"

[[providers]]
name = "${REASONIX_PROVIDER_ID}"
kind = "openai"
base_url = "${options.baseURL}"
model = "${options.modelID}"
api_key_env = "LEAPMUX_E2E_MODEL_API_KEY"
context_window = 128000
max_output_tokens = 16000
reasoning_protocol = "openai"
supported_efforts = ["low", "medium", "high"]
default_effort = "high"
vision_models = ["${options.modelID}"]

[[providers]]
name = "${options.alternateProviderID}"
kind = "openai"
base_url = "${options.baseURL}"
model = "${options.alternateModelID}"
api_key_env = "LEAPMUX_E2E_MODEL_API_KEY"
context_window = 128000
max_output_tokens = 16000
reasoning_protocol = "openai"
supported_efforts = ["low", "medium", "high"]
default_effort = "high"
`
}
