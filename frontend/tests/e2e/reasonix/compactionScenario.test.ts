import { mkdirSync, mkdtempSync, readFileSync, rmSync } from 'node:fs'
import { join, resolve } from 'node:path'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { createMockAgentEnvironment, MOCK_MODELS, MODEL_KEY, REASONIX_ALT_PROVIDER_ID, REASONIX_PLAIN_PROVIDER_ID } from '../helpers/mockAgentEnvironment'
import { prepareCompactionReasonix, REASONIX_COMPACTION_CONTEXT_WINDOW } from './compactionScenario'

vi.mock('../helpers/mockAgentEnvironment', async original => ({
  ...await original<typeof import('../helpers/mockAgentEnvironment')>(),
  createMockAgentEnvironment: vi.fn(),
}))

let directory: string
beforeEach(() => {
  const scratch = resolve(import.meta.dirname, '../../../../.tmp')
  mkdirSync(scratch, { recursive: true })
  directory = mkdtempSync(join(scratch, 'reasonix-compaction-fixture-'))
})
afterEach(() => {
  vi.mocked(createMockAgentEnvironment).mockReset()
  rmSync(directory, { recursive: true, force: true })
})

describe('prepareCompactionReasonix', () => {
  it('writes all native context windows and retains the isolated model configuration', async () => {
    const homeDir = join(directory, 'home')
    vi.mocked(createMockAgentEnvironment).mockResolvedValue({
      homeDir,
      piAgentDir: join(homeDir, 'pi'),
      ohMyPiAgentDir: join(homeDir, 'omp'),
      env: { HOME: homeDir, PATH: 'private-tools', LEAPMUX_E2E_MODEL_API_KEY: MODEL_KEY },
    })
    const result = await prepareCompactionReasonix(directory, 'http://127.0.0.1:4567')
    expect(result.agentEnv).toMatchObject({ HOME: homeDir, PATH: 'private-tools', REASONIX_HOME: join(homeDir, '.reasonix') })
    const config = readFileSync(join(homeDir, '.reasonix', 'config.toml'), 'utf8')
    const tables = config.split('[[providers]]').slice(1)
    expect(tables).toHaveLength(3)
    for (const [index, provider] of ['deepseek', REASONIX_ALT_PROVIDER_ID, REASONIX_PLAIN_PROVIDER_ID].entries()) {
      expect(tables[index]).toContain(`name = "${provider}"`)
      expect(tables[index]).toContain(`context_window = ${REASONIX_COMPACTION_CONTEXT_WINDOW}`)
      expect(tables[index]).toContain('base_url = "http://127.0.0.1:4567/v1"')
      expect(tables[index]).toContain(`model = "${[MOCK_MODELS.deepseek, MOCK_MODELS.pi, MOCK_MODELS.zai][index]}"`)
    }
    expect(config).toContain(`default_model = "deepseek/${MOCK_MODELS.deepseek}"`)
    expect(readFileSync(join(homeDir, '.reasonix', '.env'), 'utf8')).toBe(`LEAPMUX_E2E_MODEL_API_KEY=${MODEL_KEY}\n`)
  })

  it('propagates failure to prepare the isolated environment', async () => {
    vi.mocked(createMockAgentEnvironment).mockRejectedValue(new Error('private environment failed'))
    await expect(prepareCompactionReasonix(directory, 'http://127.0.0.1:4567')).rejects.toThrow('private environment failed')
  })
})
