import { existsSync, mkdirSync, readFileSync, unlinkSync, writeFileSync } from 'node:fs'
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
    reasoning: id === options.plainModelID
      ? { enabled: false }
      : {
          enabled: true,
          variants: ['low', 'medium', 'high'],
          defaultVariant: 'high',
        },
    limit: { context: 128_000, output: 16_000 },
    modalities: {
      input: ['text'],
      output: ['text'],
    },
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
        models: Object.fromEntries(zcodeModels(options).map((id, priority) => [
          id,
          model(id, priority),
        ])),
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
        providerRules: [
          {
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
          },
        ],
      },
      modelConfigRules: {
        providerModelRules: models.map(id => ({
          providerId: providerID,
          modelId: id,
          config: { enabled: true },
        })),
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

/** The paths of one run's ZCode configuration files. */
function zcodeConfigurationPaths(homeDir: string): { legacyPath: string, personalPath: string } {
  const zcodeDir = join(homeDir, '.zcode', 'v2')
  return {
    legacyPath: join(zcodeDir, 'config.json'),
    personalPath: join(zcodeDir, 'provider_config.json'),
  }
}

/** The legacy configuration shape that the revocation reads. */
interface ZcodeLegacyConfiguration {
  provider?: Record<string, { models?: Record<string, unknown> }>
}

/** The personal configuration shape that the revocation and the refresh read. */
interface ZcodePersonalConfiguration {
  config: {
    providerConfigRules: {
      providerRules: Array<{ config: { personalModelIds: string[], modelOrder: string[] } }>
    }
    modelConfigRules: {
      providerModelRules: Array<{ modelId: string }>
    }
    defaultModelSelection: { providerId: string, modelId: string, options?: Record<string, unknown> }
  }
}

/**
 * Remove one model from both ZCode configuration files, so a resumed or reopened agent must run another model. A file
 * the run does not hold is left alone, and a default selection of the removed model moves to the first remaining model.
 */
export function revokeZcodeModelFromConfiguration(homeDir: string, modelId: string): void {
  const { legacyPath, personalPath } = zcodeConfigurationPaths(homeDir)
  if (existsSync(legacyPath)) {
    const legacy = JSON.parse(readFileSync(legacyPath, 'utf8')) as ZcodeLegacyConfiguration
    for (const provider of Object.values(legacy.provider ?? {}))
      delete provider.models?.[modelId]
    writePrivateJSON(legacyPath, legacy)
  }
  if (existsSync(personalPath)) {
    const personal = JSON.parse(readFileSync(personalPath, 'utf8')) as ZcodePersonalConfiguration
    for (const rule of personal.config.providerConfigRules.providerRules) {
      rule.config.personalModelIds = rule.config.personalModelIds.filter(id => id !== modelId)
      rule.config.modelOrder = rule.config.modelOrder.filter(id => id !== modelId)
    }
    personal.config.modelConfigRules.providerModelRules
      = personal.config.modelConfigRules.providerModelRules.filter(rule => rule.modelId !== modelId)
    const selection = personal.config.defaultModelSelection
    if (selection.modelId === modelId) {
      const [firstRemaining] = personal.config.providerConfigRules.providerRules[0]?.config.personalModelIds ?? []
      if (firstRemaining !== undefined)
        selection.modelId = firstRemaining
    }
    writePrivateJSON(personalPath, personal)
  }
}

/**
 * Point the default model selection of the personal provider at another model. The change is live: the next agent to
 * start reads the refreshed configuration.
 */
export function refreshZcodeDefaultModel(homeDir: string, providerID: string, modelId: string): void {
  const { personalPath } = zcodeConfigurationPaths(homeDir)
  const personal = JSON.parse(readFileSync(personalPath, 'utf8')) as ZcodePersonalConfiguration
  personal.config.defaultModelSelection.providerId = providerID
  personal.config.defaultModelSelection.modelId = modelId
  writePrivateJSON(personalPath, personal)
}

/** Restore both configuration files after every scenario outcome. */
export async function withZcodeConfigurationRestored<T>(homeDir: string, use: () => Promise<T>): Promise<T> {
  const { legacyPath, personalPath } = zcodeConfigurationPaths(homeDir)
  const files = [legacyPath, personalPath].map(path => ({
    path,
    bytes: readFileSync(path),
  }))
  let outcome: { value: T } | { error: unknown }
  try {
    outcome = { value: await use() }
  }
  catch (error) {
    outcome = { error }
  }
  const restorationErrors: unknown[] = []
  for (const file of files) {
    try {
      writeFileSync(file.path, file.bytes, { mode: 0o600 })
    }
    catch (error) {
      restorationErrors.push(error)
    }
  }
  if (restorationErrors.length > 0) {
    if ('error' in outcome)
      restorationErrors.unshift(outcome.error)
    throw new AggregateError(restorationErrors, 'The scenario could not restore its ZCode configuration files.')
  }
  if ('error' in outcome)
    throw outcome.error
  return outcome.value
}

/** Run a scenario with the personal provider configuration alone. */
export async function withZcodePersonalProviderConfiguration<T>(homeDir: string, use: () => Promise<T>): Promise<T> {
  return withZcodeConfigurationRestored(homeDir, async () => {
    unlinkSync(zcodeConfigurationPaths(homeDir).legacyPath)
    return use()
  })
}
