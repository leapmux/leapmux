import type { Page } from '@playwright/test'
import type { ManagedNativeScenarioContext } from './nativeScenario'
import type { ProviderAgent } from './workspace'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { agentOpenOptions } from '../agentSettings'
import { openNativeAgent, requireOwnProviderAgent } from './nativeAgentOpen'

const opened = vi.hoisted(() => ({ events: [] as string[], open: vi.fn(), selected: 'agent-1' }))
vi.mock('./api', () => ({ openAgentViaAPI: opened.open }))
vi.mock('./runDirectory', async importOriginal => ({
  ...await importOriginal<typeof import('./runDirectory')>(),
  createTestDirectory: (prefix: string) => {
    opened.events.push(`directory ${prefix}`)
    return `/run/${prefix}directory`
  },
}))
vi.mock('./ui', async importOriginal => ({
  ...await importOriginal<typeof import('./ui')>(),
  openWorkspace: async (_page: unknown, workspaceId: string) => { opened.events.push(`workspace ${workspaceId}`) },
}))
vi.mock('./nativeScenario', async importOriginal => ({
  ...await importOriginal<typeof import('./nativeScenario')>(),
  selectedAgentTabId: async () => {
    opened.events.push('selected tab')
    return opened.selected
  },
}))

beforeEach(() => {
  opened.events = []
  opened.selected = 'agent-1'
  opened.open.mockReset()
  opened.open.mockImplementation(async (_hub: string, _cookie: string, _worker: string, _workspace: string, directory: string) => {
    opened.events.push(`open ${directory}`)
    return 'agent-1'
  })
})

const context = {
  page: {} as Page,
  provider: AgentProvider.CURSOR,
  workspaceId: 'workspace-1',
  leapmuxServer: { hubUrl: 'http://hub', adminToken: 'token', workerId: 'worker-1' },
} as unknown as ManagedNativeScenarioContext

/** A provider with no rule for its working directory. */
const cursor: ProviderAgent = { provider: AgentProvider.CURSOR, prefix: 'cursor-e2e' }

/** A provider whose rule makes each working directory the root of a git repository of its own. */
function repositoryAgent() {
  const workingDir = vi.fn((prefix: string) => {
    opened.events.push(`repository ${prefix}`)
    return `/run/${prefix}repository/repo`
  })
  return { agent: { provider: AgentProvider.CURSOR, prefix: 'cursor-e2e', workingDir } satisfies ProviderAgent, workingDir }
}

describe('openNativeAgent', () => {
  it('opens the agent in a fresh directory, shows the workspace, and returns the agent and the directory', async () => {
    expect(await openNativeAgent(context, cursor, { directoryPrefix: 'native-code-execution-' })).toEqual({ agentId: 'agent-1', workingDir: '/run/native-code-execution-directory' })
    expect(opened.open).toHaveBeenCalledWith('http://hub', 'token', 'worker-1', 'workspace-1', '/run/native-code-execution-directory', agentOpenOptions(AgentProvider.CURSOR))
    expect(opened.events).toEqual(['directory native-code-execution-', 'open /run/native-code-execution-directory', 'workspace workspace-1', 'selected tab'])
  })

  it('creates the new directory by the working directory rule of the provider, with the stated prefix', async () => {
    const { agent, workingDir } = repositoryAgent()
    expect(await openNativeAgent(context, agent, { directoryPrefix: 'native-code-execution-' })).toEqual({ agentId: 'agent-1', workingDir: '/run/native-code-execution-repository/repo' })
    expect(workingDir).toHaveBeenCalledExactlyOnceWith('native-code-execution-')
    expect(opened.events).toEqual(['repository native-code-execution-', 'open /run/native-code-execution-repository/repo', 'workspace workspace-1', 'selected tab'])
  })

  it('gives the rule of the provider the default prefix when the caller states none', async () => {
    const { agent, workingDir } = repositoryAgent()
    await openNativeAgent(context, agent)
    expect(workingDir).toHaveBeenCalledExactlyOnceWith('native-agent-')
  })

  it('refuses the agent of another provider before it creates a directory or opens an agent', async () => {
    const { agent, workingDir } = repositoryAgent()
    await expect(openNativeAgent(context, { ...agent, provider: AgentProvider.CLINE })).rejects.toThrow(`A native agent of provider ${AgentProvider.CURSOR} opens by its own rule, not by the rule of provider ${AgentProvider.CLINE}.`)
    expect(workingDir).not.toHaveBeenCalled()
    expect(opened.events).toEqual([])
    expect(opened.open).not.toHaveBeenCalled()
  })

  it('creates no directory and opens no agent when the merge rule refuses an override', async () => {
    const { agent, workingDir } = repositoryAgent()
    await expect(openNativeAgent(context, agent, { overrides: { model: ' ' } })).rejects.toThrow('a model override needs a model ID')
    expect(workingDir).not.toHaveBeenCalled()
    expect(opened.events).toEqual([])
    expect(opened.open).not.toHaveBeenCalled()
  })

  it('shows no workspace when the agent does not open', async () => {
    opened.open.mockRejectedValue(new Error('the Worker refused the agent'))
    await expect(openNativeAgent(context, cursor)).rejects.toThrow('the Worker refused the agent')
    expect(opened.events).toEqual(['directory native-agent-'])
  })

  it('opens the agent in an existing directory, and neither the rule of the provider nor the run creates one', async () => {
    const { agent, workingDir } = repositoryAgent()
    expect(await openNativeAgent(context, agent, { workingDir: '/run/existing' })).toEqual({ agentId: 'agent-1', workingDir: '/run/existing' })
    expect(workingDir).not.toHaveBeenCalled()
    expect(opened.events).toEqual(['open /run/existing', 'workspace workspace-1', 'selected tab'])
  })

  it('refuses an existing directory and a prefix together, before it opens an agent', async () => {
    await expect(openNativeAgent(context, cursor, { workingDir: '/run/existing', directoryPrefix: 'native-' })).rejects.toThrow('not in both')
    expect(opened.events).toEqual([])
    expect(opened.open).not.toHaveBeenCalled()
  })

  // The poll of the selected tab gives up after the default expect timeout, so the test allows more than that.
  it('fails when an earlier agent stays the selected tab', async () => {
    opened.selected = 'agent-0'
    await expect(openNativeAgent(context, cursor)).rejects.toThrow('the new native agent is the selected tab')
  }, 30_000)
})

describe('requireOwnProviderAgent', () => {
  it('accepts the agent of the provider of the context', () => {
    expect(() => requireOwnProviderAgent(context, cursor)).not.toThrow()
  })

  it('refuses the agent of another provider', () => {
    expect(() => requireOwnProviderAgent(context, { provider: AgentProvider.QODER, prefix: 'qoder-e2e' })).toThrow(`not by the rule of provider ${AgentProvider.QODER}`)
  })
})
