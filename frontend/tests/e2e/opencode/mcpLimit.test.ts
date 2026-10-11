import type { Page } from '@playwright/test'
import type { ModelScript } from '../helpers/modelScriptFixture'
import type { ManagedNativeScenarioContext } from '../helpers/nativeScenario'
import type { NativeStartupWrapper } from '../helpers/nativeStartupWrapper'
import type { NativeWorker } from '../helpers/nativeWorker'
import type { ProviderAgent } from '../helpers/workspace'
import { existsSync, mkdirSync, mkdtempSync, rmSync } from 'node:fs'
import { join, resolve } from 'node:path'
import process from 'node:process'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { unitWorkingDir } from '~/test-support/unitWorkingDir'
import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { agentOpenOptions } from '../agentSettings'
import { mcpProbeServer } from '../helpers/mcpProbeServer'
import { exerciseOpencodeMcpInputLimit, opencodeMcpServerConfiguration } from './mcpLimit'

/** The calls that the mocked Worker, launch, and agent open receive. */
const limit = vi.hoisted(() => ({
  /** The run directory of the current test, where a fresh directory of the run lands. */
  run: '',
  open: vi.fn<typeof import('../helpers/api').openAgentViaAPI>(),
  startupWorker: vi.fn<typeof import('../helpers/nativeStartupWorker').withNativeStartupWorker>(),
}))

vi.mock('../helpers/api', async importOriginal => ({
  ...await importOriginal<typeof import('../helpers/api')>(),
  openAgentViaAPI: limit.open,
}))
vi.mock('../helpers/runDirectory', async importOriginal => ({
  ...await importOriginal<typeof import('../helpers/runDirectory')>(),
  createTestDirectory: (prefix: string) => mkdtempSync(join(limit.run, prefix)),
}))
// The controlled startup Worker is a real process. These tests prove only where the scenario opens its agent.
vi.mock('../helpers/nativeStartupWorker', async importOriginal => ({
  ...await importOriginal<typeof import('../helpers/nativeStartupWorker')>(),
  withNativeStartupWorker: limit.startupWorker,
}))
// The launch names an executable that no test machine needs.
vi.mock('../helpers/nativeStartupWrapper', async importOriginal => ({
  ...await importOriginal<typeof import('../helpers/nativeStartupWrapper')>(),
  resolveNativeStartupLaunch: () => ({ binaryName: 'opencode', executable: '/private/opencode', holdWhen: ['acp'] }),
}))

describe('opencodeMcpServerConfiguration', () => {
  it('adds the server under its own name and keeps zero and false provider values', () => {
    const original = { provider: { mock: { options: { enabled: false, maximum: 0 } } }, mcp: { old: { type: 'local', command: ['old'] } } }
    const server = mcpProbeServer('result_probe', '/run/actual-results.mjs')
    const result = JSON.parse(opencodeMcpServerConfiguration(JSON.stringify(original), server))
    expect(result.provider).toEqual(original.provider)
    expect(result.mcp).toEqual({ ...original.mcp, result_probe: { type: 'local', command: [server.command, '/run/actual-results.mjs'] } })
  })

  it('keeps the other native MCP servers and supports an initially absent server map', () => {
    const server = mcpProbeServer('form_probe', '/run/form.mjs')
    expect(JSON.parse(opencodeMcpServerConfiguration('{"provider":{}}', server)).mcp).toEqual({ form_probe: { type: 'local', command: [server.command, '/run/form.mjs'] } })
  })

  it('copies the command, so a later change of the server arguments leaves the configuration unchanged', () => {
    const args = ['/run/form.mjs']
    const server = { ...mcpProbeServer('form_probe', '/run/form.mjs'), args }
    const configuration = opencodeMcpServerConfiguration('{"provider":{}}', server)
    args.push('later')
    expect(JSON.parse(configuration).mcp.form_probe.command).toEqual([server.command, '/run/form.mjs'])
  })

  it.each([
    ['null', 'The native MCP limit requires an existing isolated provider configuration.'],
    ['{}', 'The native MCP limit requires an existing isolated provider configuration.'],
    ['{"provider":{},"mcp":[]}', 'The native MCP configuration must contain a server object.'],
    ['{invalid', SyntaxError],
  ] as const)('refuses the invalid native configuration %s', (configuration, error) => {
    expect(() => opencodeMcpServerConfiguration(configuration, mcpProbeServer('form_probe', '/run/form.mjs'))).toThrow(error)
  })

  it('refuses an incomplete server command', () => {
    const server = { ...mcpProbeServer('form_probe', '/run/form.mjs'), args: ['/run/form.mjs', ''] }
    expect(() => opencodeMcpServerConfiguration('{"provider":{}}', server)).toThrow('executable command')
  })
})

describe('exerciseOpencodeMcpInputLimit', () => {
  const scratchRoot = resolve(process.cwd(), '../.tmp')
  /** The rest of the scenario drives a browser, so the open of the agent ends it in these tests. */
  const opened = new Error('The unit test ends the MCP limit scenario at the agent open.')

  beforeEach(() => {
    mkdirSync(scratchRoot, { recursive: true })
    limit.run = mkdtempSync(join(scratchRoot, 'opencode-mcp-limit-'))
    limit.open.mockReset().mockRejectedValue(opened)
    limit.startupWorker.mockReset().mockImplementation(async (_context, _launch, _options, use) => {
      await use('private-limit-worker', {} as NativeStartupWrapper, {} as NativeWorker<ManagedNativeScenarioContext['leapmuxServer']>)
    })
  })

  afterEach(() => {
    rmSync(limit.run, { recursive: true, force: true })
  })

  it('opens the agent in the working directory that the rule of the provider creates, beside the form server', async () => {
    const workingDir = vi.fn((prefix: string) => unitWorkingDir(mkdtempSync(join(limit.run, `${prefix}rule-`))))
    const providerAgent: ProviderAgent = { provider: AgentProvider.OPENCODE, prefix: 'opencode-e2e', workingDir }
    const context: ManagedNativeScenarioContext = {
      page: {} as Page,
      modelScript: {} as ModelScript,
      provider: AgentProvider.OPENCODE,
      providerAgent,
      workspaceId: 'limit-workspace',
      leapmuxServer: { hubUrl: 'http://unused.invalid', adminToken: 'controlled-token', workerId: 'suite-worker', agentEnv: { OPENCODE_CONFIG_CONTENT: '{"provider":{}}' } },
    }
    await expect(exerciseOpencodeMcpInputLimit(context, { binaryName: 'opencode', configurationVariable: 'OPENCODE_CONFIG_CONTENT' })).rejects.toBe(opened)
    expect(workingDir).toHaveBeenCalledExactlyOnceWith('opencode-family-mcp-limit-')
    const directory = workingDir.mock.results[0]?.value
    if (typeof directory !== 'string')
      throw new Error('The rule of the provider created no directory.')
    expect(limit.open).toHaveBeenCalledExactlyOnceWith(expect.objectContaining({ hubUrl: 'http://unused.invalid', adminToken: 'controlled-token', workerId: 'private-limit-worker' }), 'limit-workspace', directory, agentOpenOptions(AgentProvider.OPENCODE))
    expect(existsSync(join(directory, 'form-server.mjs'))).toBe(true)
    const environment = limit.startupWorker.mock.calls[0]?.[2].workerEnvironment?.({} as NativeStartupWrapper)
    expect(environment?.OPENCODE_CONFIG_CONTENT).toContain(JSON.stringify(join(directory, 'form-server.mjs')))
  })
})
