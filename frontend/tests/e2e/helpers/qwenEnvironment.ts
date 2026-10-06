import type { McpProbeServer } from './mcpProbeServer'
import { mkdirSync } from 'node:fs'
import { join } from 'node:path'
import { writePrivateJSON } from './privateConfigFile'

/** The auth type the Qwen configuration selects, which qualifies its model ids. */
export const QWEN_AUTH_TYPE = 'openai'

/**
 * The switches that stop Qwen Code from calling the model outside a turn a test
 * scripts, from sending usage statistics, and from checking for and installing an
 * update. The same switches are in its `settings.json`; the environment is the
 * second guard, because Qwen reads it first.
 *
 * Qwen starts the update check from its interactive UI, never from `--acp`, so the
 * update switch covers a `qwen` that an agent's shell tool starts. Only the exact
 * value `true` counts. `general.enableAutoUpdate` in `settings.json` is the same
 * switch, and a worker cannot set it.
 */
const QWEN_QUIET_ENV: Readonly<Record<string, string>> = {
  QWEN_DISABLE_AUTO_TITLE: '1',
  QWEN_USAGE_STATISTICS_ENABLED: 'false',
  QWEN_TELEMETRY_ENABLED: 'false',
  QWEN_CODE_SKIP_UPDATE_CHECK_ONCE: 'true',
}

export interface QwenEnvironmentOptions {
  /** The isolated HOME of the run. */
  homeDir: string
  /** The OpenAI-compatible base URL of the mock, which ends in `/v1`. */
  baseURL: string
  /** The model that the configuration pins, as the endpoint receives it. */
  modelID: string
  /** A second model, so a spec can switch models. */
  alternateModelID: string
  mcpEchoServer: McpProbeServer
}

/** Point Qwen Code at the mock through its own `settings.json`, and stop every model call that no test scripts. */
export function createQwenEnvironment(options: QwenEnvironmentOptions): Record<string, string> {
  const qwenHome = join(options.homeDir, '.qwen')
  mkdirSync(qwenHome, { recursive: true })
  writePrivateJSON(join(qwenHome, 'settings.json'), qwenSettings(options))
  return {
    QWEN_HOME: qwenHome,
    ...QWEN_QUIET_ENV,
  }
}

/**
 * Qwen Code's settings: one OpenAI-compatible model, pinned to the mock.
 *
 * Qwen's own default approval mode is `auto`, whose classifier asks the model
 * before a tool runs; `default` asks the reader instead. The managed memory pass
 * and the follow-up suggestions each send a model request after a turn. The
 * to-do tool and workflows are opt-in, and a spec uses both.
 */
function qwenSettings(options: QwenEnvironmentOptions): Record<string, unknown> {
  const primary = {
    id: options.modelID,
    name: 'Qwen E2E',
    baseUrl: options.baseURL,
    envKey: 'LEAPMUX_E2E_MODEL_API_KEY',
    capabilities: { vision: true, reasoning: { thinking: true, efforts: ['low', 'medium', 'high'], defaultEffort: 'high', disableField: 'reasoning_effort' } },
    generationConfig: { contextWindowSize: 128_000, modalities: { image: true, pdf: true } },
  }
  return {
    $version: 4,
    security: { auth: { selectedType: QWEN_AUTH_TYPE } },
    model: { name: options.modelID },
    mcpServers: { [options.mcpEchoServer.name]: { command: options.mcpEchoServer.command, args: [...options.mcpEchoServer.args] } },
    modelProviders: {
      [QWEN_AUTH_TYPE]: [primary, { ...primary, id: options.alternateModelID, name: 'Qwen E2E Alternate' }],
    },
    tools: { approvalMode: 'default', todoWrite: { enabled: true }, workflowsEnabled: true },
    memory: { enableManagedAutoMemory: false, enableManagedAutoDream: false },
    ui: { enableFollowupSuggestions: false },
    privacy: { usageStatisticsEnabled: false },
    general: { enableAutoUpdate: false },
  }
}
