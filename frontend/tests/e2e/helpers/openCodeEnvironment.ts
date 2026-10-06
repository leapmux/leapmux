import type { McpProbeServer } from './mcpProbeServer'

/** One model of the mock provider block: its identifier and the name that a model menu shows. */
export interface OpenCodeFamilyModelName {
  id: string
  name: string
}

/** The mock provider that the OpenCode family reads: OpenCode, Kilo, and MiMo Code. */
export interface OpenCodeFamilyProviderOptions {
  /** The OpenAI-compatible base URL of the mock, which ends in `/v1`. */
  baseURL: string
  modelKey: string
  /** The provider identifier, which no public catalog may hold. */
  providerID: string
  /** The models of the provider. The first one is the default model. */
  models: readonly OpenCodeFamilyModelName[]
}

export interface OpenCodeEnvironmentOptions extends OpenCodeFamilyProviderOptions {
  mcpEchoServer: McpProbeServer
}

/**
 * Point OpenCode and Kilo at one inline mock provider, and close the project configuration, the telemetry, and the
 * update of each.
 */
export function createOpenCodeEnvironment(options: OpenCodeEnvironmentOptions): Record<string, string> {
  // OpenCode otherwise retains every short seed turn when it compacts. Keep
  // two recent turns so the browser can prove that the first one left context.
  const config = JSON.stringify({
    ...openCodeFamilyConfig(options, options.mcpEchoServer),
    compaction: { tail_turns: 2 },
  })
  return {
    OPENCODE_CONFIG_CONTENT: config,
    OPENCODE_DISABLE_PROJECT_CONFIG: 'true',
    KILO_CONFIG_CONTENT: config,
    KILO_DISABLE_PROJECT_CONFIG: 'true',
    // Kilo sends PostHog telemetry by default. When this variable is set, Kilo
    // sends it only for the value `all`. Kilo 7.8 reads the variable once at
    // start, and the variable outranks the configuration.
    KILO_TELEMETRY_LEVEL: 'off',
    // OpenCode and Kilo upgrade their own install from the check that their TUI
    // starts, never from `acp`. The `autoupdate` key of the inline configuration
    // above cannot carry the switch: the check reads only the global config files,
    // which the isolated HOME and XDG directories leave empty. Each of the two
    // reads `true` and `1`.
    OPENCODE_DISABLE_AUTOUPDATE: 'true',
    KILO_DISABLE_AUTOUPDATE: 'true',
  }
}

/** The inline configuration that the OpenCode family reads: the mock provider, its default model, and the echo server. */
export function openCodeFamilyConfig(provider: OpenCodeFamilyProviderOptions, mcpEchoServer: McpProbeServer): Record<string, unknown> {
  const defaultModel = provider.models[0]
  if (!defaultModel)
    throw new Error('The OpenCode family provider needs at least one model.')
  return {
    formatter: false,
    lsp: false,
    mcp: { [mcpEchoServer.name]: { type: 'local', command: [mcpEchoServer.command, ...mcpEchoServer.args] } },
    model: `${provider.providerID}/${defaultModel.id}`,
    provider: {
      [provider.providerID]: openCodeFamilyProvider(provider),
    },
  }
}

/** The mock provider block, in the shape the OpenCode family reads. */
function openCodeFamilyProvider(provider: OpenCodeFamilyProviderOptions): Record<string, unknown> {
  return {
    name: 'LeapMux E2E',
    id: provider.providerID,
    env: [],
    npm: '@ai-sdk/openai-compatible',
    models: Object.fromEntries(provider.models.map(model => [model.id, openCodeFamilyModel(model.id, model.name)])),
    options: { apiKey: provider.modelKey, baseURL: provider.baseURL },
  }
}

/** The reasoning variants of each mock model. */
const REASONING_VARIANTS = ['low', 'medium', 'high', 'xhigh', 'max'] as const

/**
 * One model of the mock provider block, with each reasoning variant.
 *
 * Each variant sends its effort to the mock model. The installed OpenCode family
 * merges a configured variant over its built-in one.
 */
function openCodeFamilyModel(id: string, name: string): Record<string, unknown> {
  return {
    id,
    name,
    attachment: false,
    reasoning: true,
    temperature: false,
    tool_call: true,
    // MiMo's read tool returns an image or a PDF only when the model declares
    // the input modality. An absent entry reads as false and the tool answers
    // with a text refusal instead of the file.
    modalities: { input: ['text', 'image', 'pdf'], output: ['text'] },
    release_date: '2026-01-01',
    limit: { context: 128_000, output: 16_000 },
    cost: { input: 0, output: 0 },
    options: {},
    variants: Object.fromEntries(REASONING_VARIANTS.map(variant => [variant, { reasoningEffort: variant }])),
  }
}
