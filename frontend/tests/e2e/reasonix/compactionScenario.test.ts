import { mkdirSync, mkdtempSync, readFileSync, rmSync } from 'node:fs'
import { join, resolve } from 'node:path'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { createMockAgentEnvironment, MOCK_MODELS, MODEL_KEY, REASONIX_ALT_PROVIDER_ID, REASONIX_PLAIN_PROVIDER_ID } from '../helpers/mockAgentEnvironment'
import { prepareCompactionReasonix, REASONIX_COMPACTION_CONTEXT_WINDOW, reasonixCompactionUserInput } from './compactionScenario'

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
    expect(result.agentEnv).toMatchObject({
      HOME: homeDir,
      PATH: 'private-tools',
      REASONIX_HOME: join(homeDir, '.reasonix'),
    })
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

describe('reasonixCompactionUserInput', () => {
  const workingDir = '/private/native/project'
  const prefix = `<workspace>\nCurrent workspace: "${workingDir}". Shell commands start there and every tool resolves relative paths from it.\nVersion control: none (not a repository).\n</workspace>\n\n`
  const suffix = '\n\n<execution-policy preset="balanced" version="3">\nverify=targeted\n</execution-policy>'
  const request = (input: string) => ({
    protocol: 'openai-chat-completions' as const,
    path: '/v1/chat/completions',
    body: {
      messages: [
        { role: 'user', content: input },
      ],
    },
  })

  it.each([
    '',
    ' ',
    '/compact',
    'Quoted command:\n/compact',
  ])('preserves the complete actual user input: %j', (input) => {
    expect(reasonixCompactionUserInput(request(`${prefix}${input}${suffix}`), workingDir)).toBe(input)
  })

  it.each([
    '/compact',
    `${prefix}/compact`,
    `/compact${suffix}`,
    `${prefix}/compact${suffix}`.replace('verify=targeted', 'verify=other'),
  ])('rejects missing or changed native wrappers: %j', (input) => {
    expect(() => reasonixCompactionUserInput(request(input), workingDir)).toThrow('audited host wrappers')
  })

  it('rejects another native working directory instead of treating quoted text as the command', () => {
    expect(() => reasonixCompactionUserInput(request(`${prefix}/compact${suffix}`), '/another/project')).toThrow('audited host wrappers')
  })
})
