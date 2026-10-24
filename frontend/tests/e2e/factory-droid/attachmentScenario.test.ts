import { mkdirSync, mkdtempSync, readFileSync, rmSync } from 'node:fs'
import { join, resolve } from 'node:path'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { createMockAgentEnvironment, DROID_MOCK_MODEL_IDS, MOCK_MODELS, MODEL_KEY } from '../helpers/mockAgentEnvironment'
import { prepareDroidAttachments } from './attachmentScenario'

vi.mock('../helpers/mockAgentEnvironment', async original => ({
  ...await original<typeof import('../helpers/mockAgentEnvironment')>(),
  createMockAgentEnvironment: vi.fn(),
}))

let directory: string

beforeEach(() => {
  const scratch = resolve(import.meta.dirname, '../../../../.tmp')
  mkdirSync(scratch, { recursive: true })
  directory = mkdtempSync(join(scratch, 'droid-attachment-fixture-'))
})

afterEach(() => {
  vi.mocked(createMockAgentEnvironment).mockReset()
  rmSync(directory, { recursive: true, force: true })
})

function environment(homeDir: string) {
  return {
    homeDir,
    piAgentDir: join(homeDir, 'pi'),
    ohMyPiAgentDir: join(homeDir, 'omp'),
    env: { HOME: homeDir, PATH: 'private-tools', LEAPMUX_E2E_MODEL_API_KEY: MODEL_KEY },
  }
}

describe('prepareDroidAttachments', () => {
  it.each(['anthropic', 'openai'] as const)('writes the native %s route and preserves the isolated environment', async (provider) => {
    const homeDir = join(directory, 'home')
    vi.mocked(createMockAgentEnvironment).mockResolvedValue(environment(homeDir))
    const result = await prepareDroidAttachments(directory, 'http://127.0.0.1:4567', provider)
    const settings = JSON.parse(readFileSync(join(homeDir, '.factory', 'settings.json'), 'utf8'))
    expect(settings.customModels).toEqual([
      expect.objectContaining({ id: DROID_MOCK_MODEL_IDS.primary, model: MOCK_MODELS.droid, provider, baseUrl: 'http://127.0.0.1:4567/v1', apiKey: MODEL_KEY }),
      expect.objectContaining({ id: DROID_MOCK_MODEL_IDS.alternate, model: MOCK_MODELS.droidAlt, provider: 'generic-chat-completion-api', baseUrl: 'http://127.0.0.1:4567/v1', apiKey: MODEL_KEY }),
    ])
    expect(result.agentEnv).toMatchObject({ HOME: homeDir, PATH: 'private-tools', FACTORY_HOME_OVERRIDE: homeDir, FACTORY_API_BASE_URL: 'http://127.0.0.1:4567/v1', FACTORY_DROID_AUTO_UPDATE_ENABLED: '0' })
  })

  it('keeps two model-route configurations in separate native homes', async () => {
    const firstHome = join(directory, 'first-home')
    const secondHome = join(directory, 'second-home')
    vi.mocked(createMockAgentEnvironment)
      .mockResolvedValueOnce(environment(firstHome))
      .mockResolvedValueOnce(environment(secondHome))
    const first = await prepareDroidAttachments(join(directory, 'first'), 'http://127.0.0.1:4567', 'generic-chat-completion-api', 'anthropic')
    const firstPath = join(firstHome, '.factory', 'settings.json')
    const before = readFileSync(firstPath, 'utf8')
    const second = await prepareDroidAttachments(join(directory, 'second'), 'http://127.0.0.1:4567', 'openai', 'generic-chat-completion-api')
    expect(first.agentEnv?.FACTORY_HOME_OVERRIDE).toBe(firstHome)
    expect(second.agentEnv?.FACTORY_HOME_OVERRIDE).toBe(secondHome)
    expect(readFileSync(firstPath, 'utf8')).toBe(before)
    expect(JSON.parse(before).customModels.map((model: { provider: string }) => model.provider)).toEqual(['generic-chat-completion-api', 'anthropic'])
    expect(JSON.parse(readFileSync(join(secondHome, '.factory', 'settings.json'), 'utf8')).customModels.map((model: { provider: string }) => model.provider)).toEqual(['openai', 'generic-chat-completion-api'])
  })

  it('propagates failure to prepare the private environment', async () => {
    vi.mocked(createMockAgentEnvironment).mockRejectedValue(new Error('private environment failed'))
    await expect(prepareDroidAttachments(directory, 'http://127.0.0.1:4567', 'anthropic')).rejects.toThrow('private environment failed')
  })
})
