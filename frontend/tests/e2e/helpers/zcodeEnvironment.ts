import { mkdirSync } from 'node:fs'
import { join } from 'node:path'
import { writePrivateJSON } from './privateConfigFile'

export interface ZcodeEnvironmentOptions {
  /** The run directory, which holds ZCode's storage. */
  runDirectory: string
  /** The isolated HOME of the run. */
  homeDir: string
  /** The OpenAI-compatible base URL of the mock, which ends in `/v1`. */
  baseURL: string
  modelKey: string
  /** The personal provider block, which qualifies each model. */
  providerID: string
  /** The default model. */
  modelID: string
  /** A second model, so a spec can switch models. */
  alternateModelID: string
  /** A third model, whose reasoning is off, so a spec can switch to a model that lacks the thought levels of the others. */
  plainModelID: string
}

/** Point ZCode at the mock through a personal provider, in both of its configuration files, with telemetry off. */
export function createZcodeEnvironment(options: ZcodeEnvironmentOptions): Record<string, string> {
  const zcodeDir = join(options.homeDir, '.zcode', 'v2')
  const storageDir = join(options.runDirectory, 'zcode-storage')
  mkdirSync(zcodeDir, { recursive: true })
  mkdirSync(storageDir, { recursive: true })
  const personalConfigPath = join(zcodeDir, 'provider_config.json')
  writePrivateJSON(join(zcodeDir, 'config.json'), zcodeLegacyConfig(options))
  writePrivateJSON(personalConfigPath, zcodePersonalConfig(options))
  return {
    ZCODE_MODEL_TELEMETRY_ENABLED: 'false',
    ZCODE_PERSONAL_PROVIDER_CONFIG_FILE: personalConfigPath,
    ZCODE_STORAGE_DIR: storageDir,
  }
}

/** The models of the personal provider, in priority order. */
function zcodeModels(options: ZcodeEnvironmentOptions): string[] {
  return [options.modelID, options.alternateModelID, options.plainModelID]
}

function zcodeLegacyConfig(options: ZcodeEnvironmentOptions): Record<string, unknown> {
  const model = (id: string, priority: number) => ({
    name: id,
    reasoning: id === options.plainModelID ? { enabled: false } : { enabled: true, variants: ['low', 'medium', 'high'], defaultVariant: 'high' },
    limit: { context: 128_000, output: 16_000 },
    modalities: { input: ['text'], output: ['text'] },
    zcode: { priority },
  })
  return {
    provider: {
      [options.providerID]: {
        name: 'LeapMux E2E',
        kind: 'openai-compatible',
        source: 'custom',
        enabled: true,
        options: { apiKey: options.modelKey, baseURL: options.baseURL },
        models: Object.fromEntries(zcodeModels(options).map((id, priority) => [id, model(id, priority)])),
      },
    },
  }
}

function zcodePersonalConfig(options: ZcodeEnvironmentOptions): Record<string, unknown> {
  const providerID = options.providerID
  const models = zcodeModels(options)
  return {
    schemaVersion: 1,
    config: {
      providerOrder: [providerID],
      providerConfigRules: {
        providerRules: [{
          providerId: providerID,
          providerName: 'LeapMux E2E',
          enabled: true,
          config: {
            group: 'standard-personal',
            access: { type: 'api-key', apiKey: options.modelKey },
            api: { type: 'openai-chat-completions', baseUrl: options.baseURL },
            personalModelIds: models,
            modelOrder: models,
            visibility: 'visible',
          },
        }],
      },
      modelConfigRules: {
        providerModelRules: models.map(id => ({ providerId: providerID, modelId: id, config: { enabled: true } })),
        manualProviderModelRules: [],
      },
      defaultModelSelection: {
        providerId: providerID,
        modelId: options.modelID,
        options: { reasoningLevel: 'high' },
      },
    },
  }
}
