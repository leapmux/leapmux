import type { Page } from '@playwright/test'
import type { AgentInfo } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import type { PrivateWorkerSetup } from './privateNativeWorkspace'
import { existsSync, mkdirSync, mkdtempSync, rmSync, writeFileSync } from 'node:fs'
import { join, resolve } from 'node:path'
import process from 'node:process'
import { create } from '@bufbuild/protobuf'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { AgentInfoSchema, AgentProvider, AgentStatus } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { currentNativeAgent } from './nativeScenario'
import { withNativeWorker } from './nativeWorker'
import { withPrivateNativeWorkspace } from './privateNativeWorkspace'
import { createTestDirectory } from './runDirectory'
import { createServerOutput } from './serverOutput'
import { loginViaToken, openWorkspace } from './ui'
import { newProviderWorkingDir, withTestWorkspace } from './workspace'

const events = vi.hoisted(() => [] as string[])

vi.mock('./nativeWorker', () => ({ withNativeWorker: vi.fn() }))
vi.mock('./nativeScenario', () => ({ currentNativeAgent: vi.fn() }))
vi.mock('./runDirectory', async importOriginal => ({
  ...await importOriginal<typeof import('./runDirectory')>(),
  createTestDirectory: vi.fn(),
}))
vi.mock('./ui', () => ({
  loginViaToken: vi.fn(async () => { events.push('login') }),
  openWorkspace: vi.fn(async (_page: Page, workspaceId: string) => { events.push(`show ${workspaceId}`) }),
}))
vi.mock('./workspace', () => ({
  newProviderWorkingDir: vi.fn((_agent: unknown, prefix: string) => {
    events.push(`working directory ${prefix}`)
    return `/private/${prefix}`
  }),
  withTestWorkspace: vi.fn(async (_server: unknown, prefix: string, use: (workspace: { workspaceId: string }) => Promise<void>) => {
    events.push(`workspace ${prefix}`)
    await use({ workspaceId: 'private-workspace' })
  }),
}))

const page = {} as Page
const suite = { hubUrl: 'http://hub.test', adminToken: 'suite-token', workerId: 'suite-worker', agentEnv: { HOME: '/suite/home' } }
const providerAgent = { provider: AgentProvider.DIRAC, prefix: 'native' }
let runDirectory: string

/** The agent that the Worker reports for the selected tab. */
function agent(id: string): AgentInfo {
  return create(AgentInfoSchema, { id, status: AgentStatus.ACTIVE, agentSessionId: 'native-session' })
}

/** Start the private Worker as the real one does: hand `use` the server with the private Worker ID. */
function startWorker() {
  vi.mocked(withNativeWorker).mockImplementation(async (server, _options, use) => {
    events.push('worker')
    await use({ server: { ...server, workerId: 'private-worker', agentEnv: server.agentEnv ?? {} }, workerId: 'private-worker', dataDir: '/private/data', output: createServerOutput() })
  })
}

function options(prepare: (directory: string) => PrivateWorkerSetup<string> | Promise<PrivateWorkerSetup<string>>) {
  return {
    prefix: 'unit-private',
    workerName: 'Unit private Worker',
    providerAgent,
    prepare,
    openAgent: vi.fn(async (_server: unknown, workspaceId: string, workingDir: string) => {
      events.push(`open in ${workspaceId} at ${workingDir}`)
      return 'opened-agent'
    }),
  }
}

beforeEach(() => {
  events.length = 0
  vi.mocked(withNativeWorker).mockReset()
  vi.mocked(currentNativeAgent).mockReset().mockImplementation(async () => {
    events.push('current agent')
    return agent('opened-agent')
  })
  vi.mocked(loginViaToken).mockClear()
  vi.mocked(openWorkspace).mockClear()
  vi.mocked(newProviderWorkingDir).mockClear()
  vi.mocked(withTestWorkspace).mockClear()
  const scratch = resolve(process.cwd(), '../.tmp')
  mkdirSync(scratch, { recursive: true })
  runDirectory = mkdtempSync(join(scratch, 'private-native-workspace-'))
  vi.mocked(createTestDirectory).mockReset().mockReturnValue(runDirectory)
})
afterEach(() => rmSync(runDirectory, { recursive: true, force: true }))

describe('withPrivateNativeWorkspace', () => {
  it('prepares the run directory, starts the Worker, opens the agent, shows it, and hands the workspace to use', async () => {
    startWorker()
    const use = vi.fn(async () => {
      events.push('use')
    })
    const run = options((directory) => {
      events.push(`prepare ${directory}`)
      return { agentEnv: { HOME: '/private/home' }, env: { PRIVATE_FLAG: '1' }, setup: 'kept-setup' }
    })
    await withPrivateNativeWorkspace(page, suite, run, use)
    expect(events).toEqual([
      `prepare ${runDirectory}`,
      'worker',
      'workspace unit-private',
      'working directory unit-private-wd-',
      'open in private-workspace at /private/unit-private-wd-',
      'login',
      'show private-workspace',
      'current agent',
      'use',
    ])
    expect(createTestDirectory).toHaveBeenCalledExactlyOnceWith('unit-private-private-')
    expect(newProviderWorkingDir).toHaveBeenCalledWith(providerAgent, 'unit-private-wd-')
    expect(withNativeWorker).toHaveBeenCalledWith(
      { ...suite, agentEnv: { HOME: '/private/home' } },
      { dataDirPrefix: 'unit-private-worker', workerName: 'Unit private Worker', env: { PRIVATE_FLAG: '1' }, privateDirectories: [runDirectory] },
      expect.any(Function),
    )
    expect(loginViaToken).toHaveBeenCalledWith(page, 'suite-token')
    expect(run.openAgent).toHaveBeenCalledWith(expect.objectContaining({ workerId: 'private-worker' }), 'private-workspace', '/private/unit-private-wd-')
    expect(use).toHaveBeenCalledExactlyOnceWith({
      workspaceId: 'private-workspace',
      server: { ...suite, workerId: 'private-worker', agentEnv: { HOME: '/private/home' } },
      agentId: 'opened-agent',
      workingDir: '/private/unit-private-wd-',
      agent: agent('opened-agent'),
      runDirectory,
      setup: 'kept-setup',
    })
  })

  it('keeps the suite agent environment and adds no variable when the preparation states neither', async () => {
    startWorker()
    await withPrivateNativeWorkspace(page, suite, options(() => ({ setup: 'none' })), async () => {})
    expect(withNativeWorker).toHaveBeenCalledWith(suite, { dataDirPrefix: 'unit-private-worker', workerName: 'Unit private Worker', privateDirectories: [runDirectory] }, expect.any(Function))
  })

  it('removes the partial run directory and starts no Worker when the preparation fails', async () => {
    const failure = new Error('The private environment failed.')
    await expect(withPrivateNativeWorkspace(page, suite, options((directory) => {
      writeFileSync(join(directory, 'partial-configuration.json'), '{}')
      throw failure
    }), async () => {})).rejects.toBe(failure)
    expect(withNativeWorker).not.toHaveBeenCalled()
    expect(existsSync(runDirectory)).toBe(false)
  })

  it('leaves the run directory to the Worker cleanup when the Worker fails, because a live Worker can still read it', async () => {
    const failure = new Error('The private Worker failed without a stop.')
    vi.mocked(withNativeWorker).mockRejectedValue(failure)
    await expect(withPrivateNativeWorkspace(page, suite, options((directory) => {
      writeFileSync(join(directory, 'worker-file.txt'), 'kept')
      return { setup: 'none' }
    }), async () => {})).rejects.toBe(failure)
    expect(existsSync(join(runDirectory, 'worker-file.txt'))).toBe(true)
  })

  it('does not run use until the Worker reports the shown agent active', async () => {
    startWorker()
    let started!: (value: AgentInfo) => void
    let entered!: () => void
    const startup = new Promise<AgentInfo>((resolve) => {
      started = resolve
    })
    const waiting = new Promise<void>((resolve) => {
      entered = resolve
    })
    vi.mocked(currentNativeAgent).mockImplementation(() => {
      entered()
      return startup
    })
    const use = vi.fn(async () => {})
    const running = withPrivateNativeWorkspace(page, suite, options(() => ({ setup: 'none' })), use)
    await waiting
    expect(use).not.toHaveBeenCalled()
    started(agent('opened-agent'))
    await running
    expect(use).toHaveBeenCalledOnce()
  })

  it('keeps a startup failure and does not run use', async () => {
    startWorker()
    const failure = new Error('The native startup failed.')
    vi.mocked(currentNativeAgent).mockRejectedValue(failure)
    const use = vi.fn(async () => {})
    await expect(withPrivateNativeWorkspace(page, suite, options(() => ({ setup: 'none' })), use)).rejects.toBe(failure)
    expect(use).not.toHaveBeenCalled()
  })

  it('refuses a page that shows another agent than the opened one', async () => {
    startWorker()
    vi.mocked(currentNativeAgent).mockResolvedValue(agent('other-agent'))
    const use = vi.fn(async () => {})
    await expect(withPrivateNativeWorkspace(page, suite, options(() => ({ setup: 'none' })), use))
      .rejects
      .toThrow('The page shows agent other-agent, not the agent opened-agent that the private workspace opened.')
    expect(use).not.toHaveBeenCalled()
  })

  it.each(['', 'a/b', '..'])('refuses the prefix %j before it creates a directory', async (prefix) => {
    await expect(withPrivateNativeWorkspace(page, suite, { ...options(() => ({ setup: 'none' })), prefix }, async () => {})).rejects.toThrow('one file-name component')
    expect(createTestDirectory).not.toHaveBeenCalled()
  })
})
