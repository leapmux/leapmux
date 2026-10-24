import { mkdirSync, mkdtempSync, readFileSync, rmSync, writeFileSync } from 'node:fs'
import { join, resolve } from 'node:path'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { createMockAgentEnvironment } from '../helpers/mockAgentEnvironment'
import { prepareReasonixPermissionRules } from './permissionScenario'

vi.mock('../helpers/mockAgentEnvironment', async original => ({
  ...await original<typeof import('../helpers/mockAgentEnvironment')>(),
  createMockAgentEnvironment: vi.fn(),
}))

let directory: string
beforeEach(() => {
  const scratch = resolve(import.meta.dirname, '../../../../.tmp')
  mkdirSync(scratch, { recursive: true })
  directory = mkdtempSync(join(scratch, 'reasonix-permission-fixture-'))
})
afterEach(() => {
  vi.mocked(createMockAgentEnvironment).mockReset()
  rmSync(directory, { recursive: true, force: true })
})

describe('prepareReasonixPermissionRules', () => {
  it('adds exact native rules without replacing the private model configuration', async () => {
    const homeDir = join(directory, 'home')
    const reasonixHome = join(homeDir, '.reasonix')
    mkdirSync(reasonixHome, { recursive: true })
    const original = 'default_model = "deepseek/private-model"\n'
    writeFileSync(join(reasonixHome, 'config.toml'), original)
    vi.mocked(createMockAgentEnvironment).mockResolvedValue({
      homeDir,
      piAgentDir: join(homeDir, 'pi'),
      ohMyPiAgentDir: join(homeDir, 'omp'),
      env: { HOME: homeDir, REASONIX_HOME: reasonixHome, PATH: 'private-tools' },
    })
    const result = await prepareReasonixPermissionRules(directory, 'http://127.0.0.1:4567')
    const config = readFileSync(join(reasonixHome, 'config.toml'), 'utf8')
    expect(config.startsWith(original)).toBe(true)
    expect(config).toContain('[permissions]\nask = ["Bash=printf reasonix-smart-ask > reasonix-smart-ask.txt"]\ndeny = ["Bash=printf reasonix-smart-denied > reasonix-smart-denied.txt"]')
    expect(result.agentEnv).toMatchObject({ HOME: homeDir, REASONIX_HOME: reasonixHome, PATH: 'private-tools' })
  })

  it('propagates a failure to prepare the private environment', async () => {
    vi.mocked(createMockAgentEnvironment).mockRejectedValue(new Error('private environment failed'))
    await expect(prepareReasonixPermissionRules(directory, 'http://127.0.0.1:4567')).rejects.toThrow('private environment failed')
  })

  it('refuses an environment that has no native model configuration directory', async () => {
    vi.mocked(createMockAgentEnvironment).mockResolvedValue({ homeDir: directory, piAgentDir: '', ohMyPiAgentDir: '', env: {} })
    await expect(prepareReasonixPermissionRules(directory, 'http://127.0.0.1:4567')).rejects.toThrow('ENOENT')
  })
})
