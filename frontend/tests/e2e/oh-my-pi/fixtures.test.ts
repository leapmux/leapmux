import { mkdirSync, mkdtempSync, readFileSync, rmSync } from 'node:fs'
import { join, resolve } from 'node:path'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { createMockAgentEnvironment, MOCK_PROVIDER_IDS, OH_MY_PI_PROFILE } from '../helpers/mockAgentEnvironment'
import { ohMyPiAgentDirectory } from '../helpers/ohMyPiEnvironment'
import { OH_MY_PI_FAST_MODEL_ID, prepareFastModeOhMyPi } from './fixtures'

vi.mock('../helpers/mockAgentEnvironment', async original => ({
  ...await original<typeof import('../helpers/mockAgentEnvironment')>(),
  createMockAgentEnvironment: vi.fn(),
}))

let directory: string

beforeEach(() => {
  const scratch = resolve(import.meta.dirname, '../../../../.tmp')
  mkdirSync(scratch, { recursive: true })
  directory = mkdtempSync(join(scratch, 'omp-fast-fixture-'))
})

afterEach(() => {
  vi.mocked(createMockAgentEnvironment).mockReset()
  rmSync(directory, { recursive: true, force: true })
})

describe('prepareFastModeOhMyPi', () => {
  it('writes native OpenAI-family models in the isolated profile and retains the mock environment', async () => {
    const homeDir = join(directory, 'home')
    vi.mocked(createMockAgentEnvironment).mockResolvedValue({
      homeDir,
      piAgentDir: join(homeDir, 'pi'),
      ohMyPiAgentDir: ohMyPiAgentDirectory(homeDir, OH_MY_PI_PROFILE),
      env: { HOME: homeDir, LEAPMUX_E2E_MODEL_API_KEY: 'unit-key', PATH: 'private-tools' },
    })
    const result = await prepareFastModeOhMyPi(directory, 'http://127.0.0.1:4567')
    const agentDir = ohMyPiAgentDirectory(homeDir, OH_MY_PI_PROFILE)
    const models = JSON.parse(readFileSync(join(agentDir, 'models.yml'), 'utf8'))
    expect(models.providers[MOCK_PROVIDER_IDS.ohMyPi]).toMatchObject({
      baseUrl: 'http://127.0.0.1:4567/v1',
      apiKey: 'LEAPMUX_E2E_MODEL_API_KEY',
      api: 'openai-completions',
    })
    expect(models.providers[MOCK_PROVIDER_IDS.ohMyPi].models.map((model: { id: string }) => model.id)).toEqual(['gpt-5.5', 'gpt-5.4'])
    expect(models.providers[MOCK_PROVIDER_IDS.ohMyPi].models.map((model: { name: string }) => model.name)).toEqual(['GPT-5.5', 'GPT-5.4'])
    const config = JSON.parse(readFileSync(join(agentDir, 'config.yml'), 'utf8'))
    expect(config.modelRoles.default).toBe(OH_MY_PI_FAST_MODEL_ID)
    expect(config.disabledProviders).toContain('openai')
    expect(result.agentEnv).toMatchObject({ HOME: homeDir, PATH: 'private-tools', LEAPMUX_E2E_MODEL_API_KEY: 'unit-key', LEAPMUX_OHMYPI_DEFAULT_MODEL: OH_MY_PI_FAST_MODEL_ID, PI_PROXY: 'http://127.0.0.1:4567' })
  })

  it('propagates a failure to prepare the isolated environment', async () => {
    vi.mocked(createMockAgentEnvironment).mockRejectedValue(new Error('private environment failed'))
    await expect(prepareFastModeOhMyPi(directory, 'http://127.0.0.1:4567')).rejects.toThrow('private environment failed')
  })
})
