import type { Page, TestInfo } from '@playwright/test'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { AgentProvider } from '~/generated/proto/leapmux/v1/agent_pb'
import { createProcessStub } from '~/test-support/childProcess'
import { unitWorkingDir } from '~/test-support/unitWorkingDir'
import { AGENT_E2E_SETTINGS } from '../agentSettings'
import { createWorkspaceViaAPI, deleteWorkspaceViaAPI, openAgentViaAPI } from './api'
import { createTestDirectory } from './runDirectory'
import { loginViaToken, openWorkspace } from './ui'
import { agentWorkspaceFixture, authenticatedAgentWorkspace, createWorkspaceWithAgentsViaAPI, openProviderAgent, showWorkspaceWithAgents, withAgentWorkspace, withTestWorkspace } from './workspace'

vi.mock('./api', () => ({ createWorkspaceViaAPI: vi.fn(), deleteWorkspaceViaAPI: vi.fn(), openAgentViaAPI: vi.fn() }))
vi.mock('./runDirectory', () => ({ createTestDirectory: vi.fn(() => '/private-directory') }))
vi.mock('./ui', () => ({ loginViaToken: vi.fn(), openWorkspace: vi.fn() }))

const server = { hubUrl: 'http://hub.test', adminToken: 'session', workerId: 'worker' }
const fastAgent = { provider: AgentProvider.FAST_AGENT, prefix: 'fastagent-e2e' }
const pi = { provider: AgentProvider.PI, prefix: 'pi-e2e' }

beforeEach(() => {
  vi.clearAllMocks()
  vi.mocked(createWorkspaceViaAPI).mockReset().mockResolvedValue('workspace')
  vi.mocked(deleteWorkspaceViaAPI).mockReset().mockResolvedValue(undefined)
  vi.mocked(openAgentViaAPI).mockReset().mockResolvedValue('agent')
  vi.mocked(loginViaToken).mockReset().mockResolvedValue(undefined)
  vi.mocked(openWorkspace).mockReset().mockResolvedValue(undefined)
})

/** The open request of the one agent that the test opened. */
function openRequest() {
  expect(openAgentViaAPI).toHaveBeenCalledOnce()
  return vi.mocked(openAgentViaAPI).mock.calls[0]![3]
}

describe('workspace fixture lifetime', () => {
  it('deletes the workspace after the fixture finishes', async () => {
    const order: string[] = []
    vi.mocked(deleteWorkspaceViaAPI).mockImplementation(async () => {
      order.push('delete')
    })
    await withTestWorkspace(server, 'test', async () => {
      order.push('use')
    })
    expect(order).toEqual(['use', 'delete'])
    expect(deleteWorkspaceViaAPI).toHaveBeenCalledExactlyOnceWith(server.hubUrl, server.adminToken, 'workspace')
  })

  it('deletes the workspace when its setup callback fails', async () => {
    const error = new Error('setup failed')
    await expect(withTestWorkspace(server, 'test', async () => {
      throw error
    })).rejects.toBe(error)
    expect(deleteWorkspaceViaAPI).toHaveBeenCalledOnce()
  })

  it('does not hide an operation failure when the hub stops', async () => {
    const hub = createProcessStub()
    const error = new Error('operation failed')
    await expect(withTestWorkspace({ ...server, hubProc: hub.proc }, 'test', async () => {
      hub.emitter.exitCode = 0
      throw error
    })).rejects.toBe(error)
    expect(deleteWorkspaceViaAPI).not.toHaveBeenCalled()
  })

  it('does not run setup or deletion when creation fails', async () => {
    const error = new Error('create failed')
    vi.mocked(createWorkspaceViaAPI).mockRejectedValueOnce(error)
    const use = vi.fn()
    await expect(withTestWorkspace(server, 'test', use)).rejects.toBe(error)
    expect(use).not.toHaveBeenCalled()
    expect(deleteWorkspaceViaAPI).not.toHaveBeenCalled()
  })

  it('opens the selected provider in a private directory before fixture use, and gives the fixture that directory', async () => {
    const order: string[] = []
    vi.mocked(openAgentViaAPI).mockImplementation(async () => {
      order.push('open')
      return 'agent'
    })
    vi.mocked(deleteWorkspaceViaAPI).mockImplementation(async () => {
      order.push('delete')
    })
    const use = vi.fn(async () => {
      order.push('use')
    })
    await withAgentWorkspace(server, { provider: AgentProvider.CODEX, prefix: 'codex' }, use)
    expect(order).toEqual(['open', 'use', 'delete'])
    expect(createTestDirectory).toHaveBeenCalledExactlyOnceWith('codex-wd-')
    expect(openAgentViaAPI).toHaveBeenCalledWith(server, 'workspace', '/private-directory', expect.objectContaining({ agentProvider: AgentProvider.CODEX }))
    expect(use).toHaveBeenCalledExactlyOnceWith({ workspaceId: 'workspace', workingDir: '/private-directory' })
  })

  it('opens the agent in the working directory that the caller creates, and makes no private one', async () => {
    const workingDir = vi.fn(() => unitWorkingDir('/repository/checkout'))
    const use = vi.fn(async () => {})
    await withAgentWorkspace(server, { provider: AgentProvider.KIRO, prefix: 'kiro', workingDir }, use)
    expect(workingDir).toHaveBeenCalledExactlyOnceWith('kiro-wd-')
    expect(createTestDirectory).not.toHaveBeenCalled()
    expect(openAgentViaAPI).toHaveBeenCalledWith(server, 'workspace', '/repository/checkout', expect.objectContaining({ agentProvider: AgentProvider.KIRO }))
    expect(use).toHaveBeenCalledExactlyOnceWith({ workspaceId: 'workspace', workingDir: '/repository/checkout' })
  })

  // The directory belongs to the workspace, so the workspace exists first and the
  // cleanup still deletes it when the directory cannot be made.
  it('creates the working directory after the workspace, and deletes the workspace when that fails', async () => {
    const error = new Error('no checkout')
    const use = vi.fn()
    await expect(withAgentWorkspace(server, {
      provider: AgentProvider.KIRO,
      prefix: 'kiro',
      workingDir: () => {
        expect(createWorkspaceViaAPI).toHaveBeenCalledOnce()
        throw error
      },
    }, use)).rejects.toBe(error)
    expect(use).not.toHaveBeenCalled()
    expect(openAgentViaAPI).not.toHaveBeenCalled()
    expect(deleteWorkspaceViaAPI).toHaveBeenCalledOnce()
  })

  it('deletes a workspace after its agent fails to open', async () => {
    const error = new Error('open failed')
    vi.mocked(openAgentViaAPI).mockRejectedValueOnce(error)
    const use = vi.fn()
    await expect(withAgentWorkspace(server, { provider: AgentProvider.CODEX, prefix: 'codex' }, use)).rejects.toBe(error)
    expect(use).not.toHaveBeenCalled()
    expect(deleteWorkspaceViaAPI).toHaveBeenCalledOnce()
  })
})

/**
 * The open request applies one merge rule: the model comes only from `model`, the pinned model is the default,
 * and the option values of the test lie over the pinned effort. A spread of pinned settings and a second
 * `optionValues` dropped the pinned effort before this rule, and a model given as an option value lost to the
 * pinned model.
 */
describe('the merge rule of an agent open request', () => {
  it('opens a workspace agent with the pinned model and keeps the pinned effort under the option values of the test', async () => {
    await withAgentWorkspace(server, { ...pi, openOptions: { optionValues: { permissionMode: 'plan' } } }, async () => {})
    expect(openRequest()).toEqual({
      agentProvider: AgentProvider.PI,
      model: AGENT_E2E_SETTINGS[AgentProvider.PI].model,
      optionValues: { effort: AGENT_E2E_SETTINGS[AgentProvider.PI].effort, permissionMode: 'plan' },
    })
  })

  it('opens a workspace agent with the model of the test', async () => {
    await withAgentWorkspace(server, { ...pi, openOptions: { model: 'another-model' } }, async () => {})
    expect(openRequest()).toMatchObject({ model: 'another-model', optionValues: { effort: AGENT_E2E_SETTINGS[AgentProvider.PI].effort } })
  })

  it('refuses a model given as an option value before it creates a workspace', async () => {
    const use = vi.fn()
    await expect(withAgentWorkspace(server, { ...fastAgent, openOptions: { optionValues: { model: 'another-model' } } }, use)).rejects.toThrow('give the model as `model`')
    expect(createWorkspaceViaAPI).not.toHaveBeenCalled()
    expect(openAgentViaAPI).not.toHaveBeenCalled()
    expect(use).not.toHaveBeenCalled()
  })

  it('opens one more agent with the model of the test as the selected model', async () => {
    await openProviderAgent(server, 'workspace', fastAgent, { model: 'another-model' })
    expect(openRequest()).toEqual({ agentProvider: AgentProvider.FAST_AGENT, model: 'another-model', optionValues: {} })
  })

  it('opens one more agent with the pinned model when the test selects another option', async () => {
    await openProviderAgent(server, 'workspace', fastAgent, { optionValues: { mode: 'plan' } })
    expect(openRequest()).toEqual({
      agentProvider: AgentProvider.FAST_AGENT,
      model: AGENT_E2E_SETTINGS[AgentProvider.FAST_AGENT].model,
      optionValues: { mode: 'plan' },
    })
  })

  it('refuses a model given as an option value before it creates or prepares a directory', async () => {
    const prepare = vi.fn()
    await expect(openProviderAgent(server, 'workspace', fastAgent, { optionValues: { model: 'another-model' }, prepare })).rejects.toThrow('give the model as `model`')
    expect(createTestDirectory).not.toHaveBeenCalled()
    expect(prepare).not.toHaveBeenCalled()
    expect(openAgentViaAPI).not.toHaveBeenCalled()
  })
})

describe('openProviderAgent', () => {
  it('opens the agent in a new directory of the provider and returns the agent and its directory', async () => {
    expect(await openProviderAgent(server, 'workspace', pi)).toEqual({ agentId: 'agent', workingDir: '/private-directory' })
    expect(createTestDirectory).toHaveBeenCalledExactlyOnceWith('pi-e2e-wd-')
    expect(openAgentViaAPI).toHaveBeenCalledWith(server, 'workspace', '/private-directory', expect.objectContaining({ agentProvider: AgentProvider.PI }))
  })

  it('makes the directory through the maker of the provider, with the default prefix of the provider', async () => {
    const workingDir = vi.fn(() => unitWorkingDir('/repository/checkout'))
    expect((await openProviderAgent(server, 'workspace', { provider: AgentProvider.KIRO, prefix: 'kiro-e2e', workingDir })).workingDir).toBe('/repository/checkout')
    expect(workingDir).toHaveBeenCalledExactlyOnceWith('kiro-e2e-wd-')
    expect(createTestDirectory).not.toHaveBeenCalled()
  })

  it('gives the stated directory prefix to the maker of the provider', async () => {
    const workingDir = vi.fn(() => unitWorkingDir('/repository/checkout'))
    await openProviderAgent(server, 'workspace', { provider: AgentProvider.KIRO, prefix: 'kiro-e2e', workingDir }, { directoryPrefix: 'native-code-' })
    expect(workingDir).toHaveBeenCalledExactlyOnceWith('native-code-')
  })

  it('gives the stated directory prefix to a fresh directory of the run', async () => {
    await openProviderAgent(server, 'workspace', pi, { directoryPrefix: 'native-code-' })
    expect(createTestDirectory).toHaveBeenCalledExactlyOnceWith('native-code-')
  })

  it('refuses a directory of the test and a prefix together, before it makes a directory or opens the agent', async () => {
    const workingDir = vi.fn(() => unitWorkingDir('/unused'))
    await expect(openProviderAgent(server, 'workspace', { ...pi, workingDir }, { workingDir: unitWorkingDir('/test/directory'), directoryPrefix: 'native-' })).rejects.toThrow('not in both')
    expect(workingDir).not.toHaveBeenCalled()
    expect(createTestDirectory).not.toHaveBeenCalled()
    expect(openAgentViaAPI).not.toHaveBeenCalled()
  })

  it('opens the agent in the directory of the test, and makes no other one', async () => {
    const maker = vi.fn(() => unitWorkingDir('/unused'))
    expect((await openProviderAgent(server, 'workspace', { ...pi, workingDir: maker }, { workingDir: unitWorkingDir('/test/directory') })).workingDir).toBe('/test/directory')
    expect(maker).not.toHaveBeenCalled()
    expect(createTestDirectory).not.toHaveBeenCalled()
  })

  it('prepares the working directory before the agent opens', async () => {
    const order: string[] = []
    vi.mocked(openAgentViaAPI).mockImplementation(async () => {
      order.push('open')
      return 'agent'
    })
    await openProviderAgent(server, 'workspace', pi, { prepare: (directory) => {
      order.push(`prepare ${directory}`)
    } })
    expect(order).toEqual(['prepare /private-directory', 'open'])
  })

  it('does not open the agent when its preparation fails', async () => {
    const error = new Error('no configuration')
    await expect(openProviderAgent(server, 'workspace', pi, { prepare: () => {
      throw error
    } })).rejects.toBe(error)
    expect(openAgentViaAPI).not.toHaveBeenCalled()
  })
})

describe('createWorkspaceWithAgentsViaAPI', () => {
  it('creates the workspace and then opens one agent with the provider default', async () => {
    const order: string[] = []
    vi.mocked(createWorkspaceViaAPI).mockImplementation(async (_hub, _token, title) => {
      order.push(`create ${title}`)
      return 'workspace'
    })
    vi.mocked(openAgentViaAPI).mockImplementation(async () => {
      order.push('open')
      return 'agent-1'
    })
    await expect(createWorkspaceWithAgentsViaAPI(server, 'Docs')).resolves.toEqual({ workspaceId: 'workspace', agentIds: ['agent-1'] })
    expect(order).toEqual(['create Docs', 'open'])
    // No open options: the agent takes the provider default, as a hand-built workspace did.
    expect(openAgentViaAPI).toHaveBeenCalledExactlyOnceWith(server, 'workspace', undefined, undefined)
  })

  it('opens one agent with each title, in title order, in the working directory of the caller', async () => {
    let next = 0
    vi.mocked(openAgentViaAPI).mockImplementation(async () => `agent-${++next}`)
    const created = await createWorkspaceWithAgentsViaAPI(server, 'Docs', { agentTitles: ['Source Agent', 'Source Agent', 'Other Agent'], workingDir: '/repo' })
    expect(created).toEqual({ workspaceId: 'workspace', agentIds: ['agent-1', 'agent-2', 'agent-3'] })
    expect(vi.mocked(openAgentViaAPI).mock.calls.map(call => [call[2], call[3]])).toEqual([
      ['/repo', { title: 'Source Agent' }],
      ['/repo', { title: 'Source Agent' }],
      ['/repo', { title: 'Other Agent' }],
    ])
  })

  it('creates an empty workspace for an empty title list', async () => {
    await expect(createWorkspaceWithAgentsViaAPI(server, 'Empty', { agentTitles: [] })).resolves.toEqual({ workspaceId: 'workspace', agentIds: [] })
    expect(openAgentViaAPI).not.toHaveBeenCalled()
  })

  it.each(['', '  '])('refuses the agent title %j before it creates a workspace', async (agentTitle) => {
    await expect(createWorkspaceWithAgentsViaAPI(server, 'Bad', { agentTitles: ['Good', agentTitle] })).rejects.toThrow('visible text')
    expect(createWorkspaceViaAPI).not.toHaveBeenCalled()
    expect(openAgentViaAPI).not.toHaveBeenCalled()
  })

  it('opens the agents one after another, in the working directory of the caller, and returns them in open order', async () => {
    let next = 0
    vi.mocked(openAgentViaAPI).mockImplementation(async () => `agent-${++next}`)
    const created = await createWorkspaceWithAgentsViaAPI(server, 'Docs', { agentCount: 3, workingDir: '/repo' })
    expect(created.agentIds).toEqual(['agent-1', 'agent-2', 'agent-3'])
    for (const call of vi.mocked(openAgentViaAPI).mock.calls)
      expect(call[2]).toBe('/repo')
  })

  it('creates an empty workspace for an agent count of zero', async () => {
    await expect(createWorkspaceWithAgentsViaAPI(server, 'Empty', { agentCount: 0 })).resolves.toEqual({ workspaceId: 'workspace', agentIds: [] })
    expect(openAgentViaAPI).not.toHaveBeenCalled()
  })

  it.each([-1, 1.5, Number.NaN])('refuses the agent count %s before it creates a workspace', async (agentCount) => {
    await expect(createWorkspaceWithAgentsViaAPI(server, 'Bad', { agentCount })).rejects.toThrow(RangeError)
    expect(createWorkspaceViaAPI).not.toHaveBeenCalled()
  })

  it('deletes nothing, because the per-test reset of the suite hub owns the cleanup', async () => {
    await createWorkspaceWithAgentsViaAPI(server, 'Docs')
    expect(deleteWorkspaceViaAPI).not.toHaveBeenCalled()
  })
})

describe('showWorkspaceWithAgents', () => {
  it('creates the workspace with its agent, then signs the page in and shows that workspace', async () => {
    const order: string[] = []
    const page = {} as Page
    vi.mocked(openAgentViaAPI).mockImplementation(async (_server, _workspace, workingDir) => {
      order.push(`open in ${workingDir}`)
      return 'agent'
    })
    vi.mocked(loginViaToken).mockImplementation(async () => {
      order.push('login')
    })
    vi.mocked(openWorkspace).mockImplementation(async (_page, workspaceId) => {
      order.push(`show ${workspaceId}`)
    })
    await expect(showWorkspaceWithAgents(page, server, 'Docs', { workingDir: '/repo' })).resolves.toEqual({ workspaceId: 'workspace', agentIds: ['agent'] })
    expect(order).toEqual(['open in /repo', 'login', 'show workspace'])
    expect(loginViaToken).toHaveBeenCalledWith(page, server.adminToken)
  })

  it('signs nothing in when the workspace cannot be created', async () => {
    vi.mocked(createWorkspaceViaAPI).mockRejectedValueOnce(new Error('create refused'))
    await expect(showWorkspaceWithAgents({} as Page, server, 'Docs')).rejects.toThrow('create refused')
    expect(loginViaToken).not.toHaveBeenCalled()
    expect(openWorkspace).not.toHaveBeenCalled()
  })
})

describe('agentWorkspaceFixture', () => {
  it('carries the agent and the working directory that the fixture opened it in', () => {
    expect(agentWorkspaceFixture({ workspaceId: 'workspace' }, 'agent', '/repo')).toEqual({ workspaceId: 'workspace', agentId: 'agent', workingDir: '/repo' })
  })

  it('leaves the working directory out for the Worker default, which the test does not know', () => {
    const fixture = agentWorkspaceFixture({ workspaceId: 'workspace' }, 'agent', undefined)
    expect(fixture).toEqual({ workspaceId: 'workspace', agentId: 'agent' })
    expect('workingDir' in fixture).toBe(false)
  })
})

describe('authenticatedAgentWorkspace', () => {
  const page = {} as Page
  const leapmuxServer: typeof server & { agentEnv: Record<string, string> } = { ...server, agentEnv: { PROVIDER_HOME: '/provider-home' } }

  /** Run the fixture as Playwright runs it, with a test info that reports the outcome of the test. */
  async function run(options: Parameters<typeof authenticatedAgentWorkspace>[0], body: () => Promise<void>, failed: boolean) {
    const testInfo = { status: 'passed', expectedStatus: 'passed', attach: vi.fn(async () => {}) }
    const fixture = authenticatedAgentWorkspace(options)
    const outcome = fixture({ page, leapmuxServer }, async (workspace) => {
      expect(workspace).toEqual({ workspaceId: 'workspace', workingDir: '/private-directory' })
      await body()
      if (failed)
        testInfo.status = 'failed'
    }, testInfo as unknown as TestInfo)
    return { outcome, testInfo }
  }

  it('opens the agent, signs in, and shows the workspace before the test runs', async () => {
    const order: string[] = []
    vi.mocked(openAgentViaAPI).mockImplementation(async () => {
      order.push('open')
      return 'agent'
    })
    vi.mocked(loginViaToken).mockImplementation(async () => {
      order.push('login')
    })
    vi.mocked(openWorkspace).mockImplementation(async () => {
      order.push('show')
    })
    const { outcome } = await run(pi, async () => {
      order.push('test')
    }, false)
    await outcome
    expect(order).toEqual(['open', 'login', 'show', 'test'])
    expect(loginViaToken).toHaveBeenCalledWith(page, server.adminToken)
    expect(openWorkspace).toHaveBeenCalledWith(page, 'workspace')
    expect(deleteWorkspaceViaAPI).toHaveBeenCalledOnce()
  })

  it('applies the open options of the fixture through the merge rule', async () => {
    const { outcome } = await run({ ...pi, openOptions: { optionValues: { permissionMode: 'yolo' } } }, async () => {}, false)
    await outcome
    expect(openRequest()).toMatchObject({ optionValues: { effort: AGENT_E2E_SETTINGS[AgentProvider.PI].effort, permissionMode: 'yolo' } })
  })

  it('runs no diagnostic after a passed test', async () => {
    const onFailure = vi.fn(async () => {})
    const { outcome } = await run({ ...pi, onFailure }, async () => {}, false)
    await outcome
    expect(onFailure).not.toHaveBeenCalled()
  })

  it('runs the diagnostic after a failed test, while the agent workspace still exists', async () => {
    const order: string[] = []
    vi.mocked(deleteWorkspaceViaAPI).mockImplementation(async () => {
      order.push('delete')
    })
    const onFailure = vi.fn(async (_testInfo: TestInfo, received: typeof leapmuxServer) => {
      order.push(`diagnose ${received.agentEnv.PROVIDER_HOME}`)
    })
    const { outcome, testInfo } = await run({ ...pi, onFailure }, async () => {}, true)
    await outcome
    expect(order).toEqual(['diagnose /provider-home', 'delete'])
    expect(onFailure).toHaveBeenCalledExactlyOnceWith(testInfo, leapmuxServer)
  })

  it('attaches a failed diagnostic and does not replace the failure of the test', async () => {
    const onFailure = vi.fn(async () => {
      throw new Error('the native log is unreadable')
    })
    const { outcome, testInfo } = await run({ ...pi, onFailure }, async () => {}, true)
    await expect(outcome).resolves.toBeUndefined()
    expect(testInfo.attach).toHaveBeenCalledExactlyOnceWith('native-diagnostics-error', expect.objectContaining({
      body: expect.stringContaining('the native log is unreadable'),
      contentType: 'text/plain',
    }))
  })

  it('keeps the error of a failed test body and still runs the diagnostic', async () => {
    const error = new Error('the test failed')
    const onFailure = vi.fn(async () => {})
    const testInfo = { status: 'failed', expectedStatus: 'passed', attach: vi.fn(async () => {}) }
    const outcome = authenticatedAgentWorkspace({ ...pi, onFailure })({ page, leapmuxServer }, async () => {
      throw error
    }, testInfo as unknown as TestInfo)
    await expect(outcome).rejects.toBe(error)
    expect(onFailure).toHaveBeenCalledOnce()
    expect(deleteWorkspaceViaAPI).toHaveBeenCalledOnce()
  })
})
