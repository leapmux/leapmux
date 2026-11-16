import { existsSync, mkdirSync, mkdtempSync, readFileSync, rmSync, statSync, writeFileSync } from 'node:fs'
import { join, resolve } from 'node:path'
import { afterEach, beforeEach, describe, expect, it } from 'vitest'
import { createZcodeEnvironment, refreshZcodeDefaultModel, revokeZcodeModelFromConfiguration, withZcodeConfigurationRestored, withZcodePersonalProviderConfiguration, withZcodeUserMcpServer } from './zcodeEnvironment'

let runDirectory: string
function options() {
  return {
    runDirectory,
    homeDir: join(runDirectory, 'home'),
    baseURL: 'http://127.0.0.1:4567/v1',
    modelKey: 'unit-key',
    providerID: 'personal:unit',
    modelID: 'unit-model',
    alternateModelID: 'unit-alt',
    plainModelID: 'unit-plain',
  }
}

beforeEach(() => {
  const scratch = resolve(import.meta.dirname, '../../../../.tmp')
  mkdirSync(scratch, { recursive: true })
  runDirectory = mkdtempSync(join(scratch, 'zcode-environment-test-'))
})

afterEach(() => rmSync(runDirectory, { recursive: true, force: true }))

describe('createZcodeEnvironment', () => {
  it('writes the provider of the three models into the legacy configuration, in priority order', () => {
    createZcodeEnvironment(options())
    const config = JSON.parse(readFileSync(join(runDirectory, 'home', '.zcode', 'v2', 'config.json'), 'utf8'))
    const provider = config.provider['personal:unit']
    expect(provider.options).toEqual({ apiKey: 'unit-key', baseURL: 'http://127.0.0.1:4567/v1' })
    expect(Object.keys(provider.models)).toEqual(['unit-model', 'unit-alt', 'unit-plain'])
    expect(provider.models['unit-model'].zcode).toEqual({ priority: 0 })
    expect(provider.models['unit-alt'].zcode).toEqual({ priority: 1 })
    expect(provider.models['unit-plain'].zcode).toEqual({ priority: 2 })
  })

  // A spec switches to the plain model to reach a model without a thought level.
  it('turns the reasoning of the plain model off, and keeps the variants of the other two', () => {
    createZcodeEnvironment(options())
    const config = JSON.parse(readFileSync(join(runDirectory, 'home', '.zcode', 'v2', 'config.json'), 'utf8'))
    const models = config.provider['personal:unit'].models
    for (const id of ['unit-model', 'unit-alt']) {
      expect(models[id].reasoning).toEqual({
        enabled: true,
        variants: ['low', 'medium', 'high'],
        defaultVariant: 'high',
      })
    }
    expect(models['unit-plain'].reasoning).toEqual({ enabled: false })
  })

  it('writes the personal provider configuration, which selects the first model by default', () => {
    const env = createZcodeEnvironment(options())
    expect(env.ZCODE_PERSONAL_PROVIDER_CONFIG_FILE).toBe(join(runDirectory, 'home', '.zcode', 'v2', 'provider_config.json'))
    const personal = JSON.parse(readFileSync(env.ZCODE_PERSONAL_PROVIDER_CONFIG_FILE!, 'utf8'))
    expect(personal.config.defaultModelSelection).toEqual({
      providerId: 'personal:unit',
      modelId: 'unit-model',
      options: { reasoningLevel: 'high' },
    })
    const rule = personal.config.providerConfigRules.providerRules[0]
    expect(rule.config.access).toEqual({ type: 'api-key', apiKey: 'unit-key' })
    expect(rule.config.personalModelIds).toEqual(['unit-model', 'unit-alt', 'unit-plain'])
    expect(personal.config.modelConfigRules.providerModelRules.map((model: { modelId: string }) => model.modelId)).toEqual(['unit-model', 'unit-alt', 'unit-plain'])
  })

  it('keeps its storage in the run, with telemetry off', () => {
    const env = createZcodeEnvironment(options())
    expect(env.ZCODE_STORAGE_DIR).toBe(join(runDirectory, 'zcode-storage'))
    expect(statSync(env.ZCODE_STORAGE_DIR!).isDirectory()).toBe(true)
    expect(env.ZCODE_MODEL_TELEMETRY_ENABLED).toBe('false')
  })
})

describe('revokeZcodeModelFromConfiguration', () => {
  it('removes the model from both configuration files and moves a default selection of it', () => {
    createZcodeEnvironment(options())
    revokeZcodeModelFromConfiguration(join(runDirectory, 'home'), 'unit-model')
    const legacy = JSON.parse(readFileSync(join(runDirectory, 'home', '.zcode', 'v2', 'config.json'), 'utf8'))
    expect(Object.keys(legacy.provider['personal:unit'].models)).toEqual(['unit-alt', 'unit-plain'])
    const personal = JSON.parse(readFileSync(join(runDirectory, 'home', '.zcode', 'v2', 'provider_config.json'), 'utf8'))
    expect(personal.config.providerConfigRules.providerRules[0].config.personalModelIds).toEqual(['unit-alt', 'unit-plain'])
    expect(personal.config.providerConfigRules.providerRules[0].config.modelOrder).toEqual(['unit-alt', 'unit-plain'])
    expect(personal.config.modelConfigRules.providerModelRules.map((model: { modelId: string }) => model.modelId)).toEqual(['unit-alt', 'unit-plain'])
    expect(personal.config.defaultModelSelection).toEqual({
      providerId: 'personal:unit',
      modelId: 'unit-alt',
      options: { reasoningLevel: 'high' },
    })
  })

  it('keeps a default selection of another model', () => {
    createZcodeEnvironment(options())
    revokeZcodeModelFromConfiguration(join(runDirectory, 'home'), 'unit-alt')
    const personal = JSON.parse(readFileSync(join(runDirectory, 'home', '.zcode', 'v2', 'provider_config.json'), 'utf8'))
    expect(personal.config.defaultModelSelection).toEqual({
      providerId: 'personal:unit',
      modelId: 'unit-model',
      options: { reasoningLevel: 'high' },
    })
  })

  // A configuration file the run does not hold is left alone: the revocation
  // still reaches the other file and never fails on the absent one.
  it('leaves a configuration file the run does not hold alone', () => {
    createZcodeEnvironment(options())
    rmSync(join(runDirectory, 'home', '.zcode', 'v2', 'config.json'))
    revokeZcodeModelFromConfiguration(join(runDirectory, 'home'), 'unit-model')
    const personal = JSON.parse(readFileSync(join(runDirectory, 'home', '.zcode', 'v2', 'provider_config.json'), 'utf8'))
    expect(personal.config.providerConfigRules.providerRules[0].config.personalModelIds).toEqual(['unit-alt', 'unit-plain'])
  })
})

describe('refreshZcodeDefaultModel', () => {
  it('points the default model selection at the given model', () => {
    createZcodeEnvironment(options())
    refreshZcodeDefaultModel(join(runDirectory, 'home'), 'personal:unit', 'unit-alt')
    const personal = JSON.parse(readFileSync(join(runDirectory, 'home', '.zcode', 'v2', 'provider_config.json'), 'utf8'))
    expect(personal.config.defaultModelSelection).toEqual({
      providerId: 'personal:unit',
      modelId: 'unit-alt',
      options: { reasoningLevel: 'high' },
    })
    expect(personal.config.providerConfigRules.providerRules[0].config.personalModelIds).toEqual(['unit-model', 'unit-alt', 'unit-plain'])
  })
})

describe('withZcodePersonalProviderConfiguration', () => {
  it.each([false, true])('restores both original files when the callback fails: %s', async (fails) => {
    createZcodeEnvironment(options())
    const homeDir = options().homeDir
    const paths = ['config.json', 'provider_config.json'].map(file => join(homeDir, '.zcode', 'v2', file))
    const before = paths.map(path => readFileSync(path))
    const failure = new Error('scenario failed')
    const operation = withZcodePersonalProviderConfiguration(homeDir, async () => {
      expect(existsSync(paths[0]!)).toBe(false)
      expect(existsSync(paths[1]!)).toBe(true)
      refreshZcodeDefaultModel(homeDir, options().providerID, options().alternateModelID)
      if (fails)
        throw failure
      return 'completed'
    })
    if (fails)
      await expect(operation).rejects.toBe(failure)
    else
      await expect(operation).resolves.toBe('completed')
    expect(paths.map(path => readFileSync(path))).toEqual(before)
  })
})

describe('withZcodeConfigurationRestored', () => {
  it('restores the second file when the first restoration and the scenario fail', async () => {
    createZcodeEnvironment(options())
    const homeDir = options().homeDir
    const legacyPath = join(homeDir, '.zcode', 'v2', 'config.json')
    const personalPath = join(homeDir, '.zcode', 'v2', 'provider_config.json')
    const original = readFileSync(personalPath)
    const failure = new Error('scenario failed')
    const operation = withZcodeConfigurationRestored(homeDir, async () => {
      writeFileSync(personalPath, 'changed')
      rmSync(legacyPath)
      mkdirSync(legacyPath)
      throw failure
    })
    await expect(operation).rejects.toMatchObject({
      errors: expect.arrayContaining([
        failure,
        expect.objectContaining({ code: 'EISDIR' }),
      ]),
    })
    expect(readFileSync(personalPath)).toEqual(original)
  })
})

describe('withZcodeUserMcpServer', () => {
  it.each([false, true])('uses the current CLI path and restores existing bytes after failure: %s', async (fails) => {
    const { homeDir } = options()
    createZcodeEnvironment(options())
    const cliDirectory = join(homeDir, '.zcode', 'cli')
    mkdirSync(cliDirectory, { recursive: true })
    const path = join(cliDirectory, 'config.json')
    const initial = {
      provider: {
        existing: { model: 'keep' },
      },
      mcp: {
        servers: {
          existing: { type: 'stdio', command: 'existing' },
        },
      },
    }
    writeFileSync(path, JSON.stringify(initial))
    const before = readFileSync(path)
    const legacyPath = join(homeDir, '.zcode', 'v2', 'config.json')
    const legacyBefore = readFileSync(legacyPath)
    const failure = new Error('The native MCP scenario failed.')
    const run = withZcodeUserMcpServer(homeDir, 'form_probe', {
      command: '/private/node',
      args: ['literal $(false) path'],
    }, async () => {
      const configured = JSON.parse(readFileSync(path, 'utf8'))
      expect(configured.provider).toEqual(initial.provider)
      expect(configured.mcp.servers).toEqual({
        existing: initial.mcp.servers.existing,
        form_probe: {
          type: 'stdio',
          command: '/private/node',
          args: ['literal $(false) path'],
          env: {},
        },
      })
      if (fails)
        throw failure
      return 'completed'
    })
    if (fails)
      await expect(run).rejects.toBe(failure)
    else
      await expect(run).resolves.toBe('completed')
    expect(readFileSync(path)).toEqual(before)
    expect(readFileSync(legacyPath)).toEqual(legacyBefore)
  })

  it.each(['success', 'throw', 'remove'])('restores an absent CLI configuration after %s', async (outcome) => {
    createZcodeEnvironment(options())
    const path = join(options().homeDir, '.zcode', 'cli', 'config.json')
    const run = withZcodeUserMcpServer(options().homeDir, 'probe', { command: 'node', args: [] }, async () => {
      expect(JSON.parse(readFileSync(path, 'utf8')).mcp.servers.probe.command).toBe('node')
      if (outcome === 'throw')
        throw new Error('The native scenario failed.')
      if (outcome === 'remove')
        rmSync(path)
      return 'completed'
    })
    if (outcome === 'throw')
      await expect(run).rejects.toThrow('The native scenario failed.')
    else
      await expect(run).resolves.toBe('completed')
    expect(existsSync(path)).toBe(false)
  })

  it.each([
    { name: '', command: 'node' },
    { name: 'probe', command: '' },
  ])('rejects an empty declaration before mutation: $name / $command', async ({ name, command }) => {
    createZcodeEnvironment(options())
    const path = join(options().homeDir, '.zcode', 'v2', 'config.json')
    const before = readFileSync(path)
    await expect(withZcodeUserMcpServer(options().homeDir, name, { command, args: [] }, async () => 'unused')).rejects.toThrow('requires a server name and command')
    expect(readFileSync(path)).toEqual(before)
  })
})
