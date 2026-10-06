import type { PrivateNativeWorkspaceOptions, PrivateWorkerHub } from '../helpers/privateNativeWorkspace'
import { mkdirSync, mkdtempSync, readFileSync, rmSync, statSync, writeFileSync } from 'node:fs'
import { join, resolve } from 'node:path'
import process from 'node:process'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { FAST_AGENT_MOCK_MODEL } from '../helpers/mockAgentEnvironment'
import { withPrivateNativeWorkspace } from '../helpers/privateNativeWorkspace'
import { openProviderAgent } from '../helpers/workspace'
import { prepareFastAgentModes } from './fixtures'
import { FAST_AGENT_AGENT } from './scenarios'

const fixtures = vi.hoisted(() => new Map<string, unknown>())
vi.mock('../fastagent-fixtures', () => ({
  fastAgentTest: { extend: (definitions: Record<string, unknown>) => {
    for (const [name, definition] of Object.entries(definitions))
      fixtures.set(name, definition)
    return {}
  } },
}))
vi.mock('../helpers/privateNativeWorkspace', () => ({ withPrivateNativeWorkspace: vi.fn() }))
vi.mock('../helpers/workspace', async original => ({
  ...await original<typeof import('../helpers/workspace')>(),
  openProviderAgent: vi.fn(),
}))

let directory: string
let sharedHome: string
beforeEach(() => {
  const scratch = resolve(process.cwd(), '../.tmp')
  mkdirSync(scratch, { recursive: true })
  directory = mkdtempSync(join(scratch, 'fast-agent-modes-fixture-'))
  sharedHome = join(directory, 'shared-home')
  mkdirSync(sharedHome)
  writeFileSync(join(sharedHome, 'fast-agent.yaml'), 'default_model: mock\n')
  vi.mocked(withPrivateNativeWorkspace).mockReset()
  vi.mocked(openProviderAgent).mockReset()
})
afterEach(() => rmSync(directory, { recursive: true, force: true }))

describe('prepareFastAgentModes', () => {
  it('copies the suite configuration and writes the reader and writer cards into a private home', () => {
    const run = join(directory, 'run')
    mkdirSync(run)
    const prepared = prepareFastAgentModes(run, sharedHome)
    const home = join(run, 'fast-agent-home')
    expect(prepared).toEqual({ env: { FAST_AGENT_HOME: home }, setup: undefined })
    expect(readFileSync(join(home, 'fast-agent.yaml'), 'utf8')).toBe('default_model: mock\n')
    const reader = readFileSync(join(home, 'agent-cards', 'reader.md'), 'utf8')
    const writer = readFileSync(join(home, 'agent-cards', 'writer.md'), 'utf8')
    expect(reader).toContain(`model: ${FAST_AGENT_MOCK_MODEL}\ndefault: true\n`)
    expect(reader).toContain('NATIVE_FAST_AGENT_READER')
    expect(writer).not.toContain('default: true')
    expect(writer).toContain('NATIVE_FAST_AGENT_WRITER')
    expect(statSync(join(home, 'agent-cards', 'reader.md')).mode & 0o777).toBe(0o600)
  })

  it('refuses a suite home with no mock configuration', () => {
    expect(() => prepareFastAgentModes(directory, join(directory, 'absent-home'))).toThrow()
  })
})

describe('fastAgentModesWorkspace', () => {
  const leapmuxServer = { hubUrl: 'http://hub.test', adminToken: 'token', workerId: 'suite-worker', agentEnv: { HOME: '/suite/home', FAST_AGENT_HOME: '' } }

  function fixture(): (fixtures: object, use: (value: unknown) => Promise<void>) => Promise<void> {
    const definition = fixtures.get('fastAgentModesWorkspace')
    if (typeof definition !== 'function')
      throw new Error('The Fast Agent modes fixture definition is absent.')
    return definition as (fixtures: object, use: (value: unknown) => Promise<void>) => Promise<void>
  }

  it('opens the agent in the reader mode in the working directory of the private workspace', async () => {
    let options: PrivateNativeWorkspaceOptions<PrivateWorkerHub, undefined> | undefined
    vi.mocked(withPrivateNativeWorkspace).mockImplementation(async (_page, _server, given) => {
      options = given as PrivateNativeWorkspaceOptions<PrivateWorkerHub, undefined>
    })
    vi.mocked(openProviderAgent).mockResolvedValue({ agentId: 'fast-agent', workingDir: '/private/wd' })
    await fixture()({ page: {}, leapmuxServer: { ...leapmuxServer, agentEnv: { ...leapmuxServer.agentEnv, FAST_AGENT_HOME: sharedHome } } }, async () => {})
    if (!options)
      throw new Error('The fixture started no private workspace.')
    expect(options).toMatchObject({ prefix: 'fast-agent-modes', workerName: 'Fast Agent modes', providerAgent: FAST_AGENT_AGENT })
    const server = { ...leapmuxServer, workerId: 'private-worker' }
    await expect(options.openAgent(server, 'private-workspace', '/private/wd')).resolves.toBe('fast-agent')
    expect(openProviderAgent).toHaveBeenCalledExactlyOnceWith(server, 'private-workspace', FAST_AGENT_AGENT, { workingDir: '/private/wd', optionValues: { permissionMode: 'reader' } })
  })

  it('refuses a suite environment with no Fast Agent home before it starts a Worker', async () => {
    await expect(fixture()({ page: {}, leapmuxServer }, async () => {})).rejects.toThrow('requires its suite mock configuration')
    expect(withPrivateNativeWorkspace).not.toHaveBeenCalled()
  })
})
