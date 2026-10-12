import { mkdirSync, writeFileSync } from 'node:fs'
import { join } from 'node:path'

export interface FastAgentEnvironmentOptions {
  /** The isolated HOME of the run. */
  homeDir: string
  /** The OpenAI-compatible base URL of the mock, which ends in `/v1`. */
  baseURL: string
  modelKey: string
  /** The default model, which fast-agent routes to its `openai` block. */
  modelID: string
  /** The default model of the `zai` block, which routes the reasoning models. */
  zaiModelID: string
}

/** Point fast-agent at the mock through the `openai` and `zai` blocks of its own configuration. */
export function createFastAgentEnvironment(options: FastAgentEnvironmentOptions): Record<string, string> {
  const fastAgentHome = join(options.homeDir, '.fast-agent')
  mkdirSync(fastAgentHome, { recursive: true })
  writeFileSync(join(fastAgentHome, 'fast-agent.yaml'), fastAgentConfig(options), { mode: 0o600 })
  return { FAST_AGENT_HOME: fastAgentHome }
}

/** fast-agent's model routing: the default model goes to this `openai` block. */
function fastAgentConfig(options: FastAgentEnvironmentOptions): string {
  return `default_model: "${options.modelID}"
openai:
  api_key: "${options.modelKey}"
  base_url: "${options.baseURL}"
zai:
  api_key: "${options.modelKey}"
  base_url: "${options.baseURL}"
  default_model: "${options.zaiModelID}"
`
}
