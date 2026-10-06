import type { Page } from '@playwright/test'
import type { ManagedNativeScenarioContext } from './nativeScenario'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { agentOpenOptions } from '../agentSettings'
import { openNativeAgent } from './nativeAgentOpen'

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

describe('openNativeAgent', () => {
  it('opens the agent in a fresh directory, shows the workspace, and returns the agent and the directory', async () => {
    expect(await openNativeAgent(context, { directoryPrefix: 'native-code-execution-' })).toEqual({ agentId: 'agent-1', workingDir: '/run/native-code-execution-directory' })
    expect(opened.open).toHaveBeenCalledWith('http://hub', 'token', 'worker-1', 'workspace-1', '/run/native-code-execution-directory', agentOpenOptions(AgentProvider.CURSOR))
    expect(opened.events).toEqual(['directory native-code-execution-', 'open /run/native-code-execution-directory', 'workspace workspace-1', 'selected tab'])
  })

  it('creates no directory and opens no agent when the merge rule refuses an override', async () => {
    await expect(openNativeAgent(context, { overrides: { model: ' ' } })).rejects.toThrow('a model override needs a model ID')
    expect(opened.events).toEqual([])
    expect(opened.open).not.toHaveBeenCalled()
  })

  it('shows no workspace when the agent does not open', async () => {
    opened.open.mockRejectedValue(new Error('the Worker refused the agent'))
    await expect(openNativeAgent(context)).rejects.toThrow('the Worker refused the agent')
    expect(opened.events).toEqual(['directory native-agent-'])
  })

  it('opens the agent in an existing directory and creates none', async () => {
    expect(await openNativeAgent(context, { workingDir: '/run/existing' })).toEqual({ agentId: 'agent-1', workingDir: '/run/existing' })
    expect(opened.events).toEqual(['open /run/existing', 'workspace workspace-1', 'selected tab'])
  })

  it('refuses an existing directory and a prefix together, before it opens an agent', async () => {
    await expect(openNativeAgent(context, { workingDir: '/run/existing', directoryPrefix: 'native-' })).rejects.toThrow('not in both')
    expect(opened.events).toEqual([])
    expect(opened.open).not.toHaveBeenCalled()
  })

  // The poll of the selected tab gives up after the default expect timeout, so the test allows more than that.
  it('fails when an earlier agent stays the selected tab', async () => {
    opened.selected = 'agent-0'
    await expect(openNativeAgent(context)).rejects.toThrow('the new native agent is the selected tab')
  }, 30_000)
})
