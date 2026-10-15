import type { Page } from '@playwright/test'
import type { ManagedNativeScenarioContext } from './nativeScenario'
import type { ProviderAgent } from './workspace'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { unitWorkingDir } from '~/test-support/unitWorkingDir'
import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { agentOpenOptions } from '../agentSettings'
import { exerciseProviderAgentButtons, newNativeWorkingDir, openNativeAgent, requireOwnProviderAgent } from './nativeAgentOpen'

const buttons = vi.hoisted(() => ({
  stage: 0,
  ids: ['original', 'from-menu', 'from-button'],
  providers: [] as number[],
  counts: [] as number[],
  button: undefined as unknown,
  visible: true,
}))

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
  agentTabs: () => ({ count: async () => 1 + buttons.stage }),
  expectAgentTabCount: async (_page: Page, count: number) => {
    buttons.counts.push(count)
    expect(count).toBe(1 + buttons.stage)
  },
  openWorkspace: async (_page: unknown, workspaceId: string) => { opened.events.push(`workspace ${workspaceId}`) },
}))
vi.mock('./nativeScenario', async importOriginal => ({
  ...await importOriginal<typeof import('./nativeScenario')>(),
  currentNativeAgent: async () => ({ id: buttons.ids[buttons.stage], agentProvider: buttons.providers[buttons.stage] }),
  selectedAgentTabId: async () => {
    opened.events.push('selected tab')
    return opened.selected
  },
}))

vi.mock('@playwright/test', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@playwright/test')>()
  const check = Object.assign((value: unknown, message?: string) => {
    if (buttons.button !== undefined && value === buttons.button) {
      return { toBeVisible: async () => {
        expect(buttons.visible, 'the provider creation button is visible').toBe(true)
      } }
    }
    return actual.expect(value, message)
  }, { poll: actual.expect.poll })
  return { ...actual, expect: check }
})

beforeEach(() => {
  buttons.stage = 0
  buttons.ids = ['original', 'from-menu', 'from-button']
  buttons.providers = [AgentProvider.CURSOR, AgentProvider.CURSOR, AgentProvider.CURSOR]
  buttons.counts = []
  buttons.button = undefined
  buttons.visible = true
  opened.events = []
  opened.selected = 'agent-1'
  opened.open.mockReset()
  opened.open.mockImplementation(async (_server: unknown, _workspace: string, directory: string) => {
    opened.events.push(`open ${directory}`)
    return 'agent-1'
  })
})

/** A provider with no rule for its working directory. */
const cursor: ProviderAgent = { provider: AgentProvider.CURSOR, prefix: 'cursor-e2e' }

/** The scenario context of a Cursor agent that opens by the rule of `providerAgent`. */
function contextOf(providerAgent: ProviderAgent = cursor): ManagedNativeScenarioContext {
  return {
    page: {} as Page,
    provider: AgentProvider.CURSOR,
    providerAgent,
    workspaceId: 'workspace-1',
    leapmuxServer: { hubUrl: 'http://hub', adminToken: 'token', workerId: 'worker-1' },
  } as unknown as ManagedNativeScenarioContext
}

const context = contextOf()

/** A provider whose rule makes each working directory the root of a git repository of its own. */
function repositoryAgent() {
  const workingDir = vi.fn((prefix: string) => {
    opened.events.push(`repository ${prefix}`)
    return unitWorkingDir(`/run/${prefix}repository/repo`)
  })
  return { agent: { provider: AgentProvider.CURSOR, prefix: 'cursor-e2e', workingDir } satisfies ProviderAgent, workingDir }
}

describe('openNativeAgent', () => {
  it('opens the agent in a fresh directory, shows the workspace, and returns the agent and the directory', async () => {
    expect(await openNativeAgent(context, { directoryPrefix: 'native-code-execution-' })).toEqual({ agentId: 'agent-1', workingDir: '/run/native-code-execution-directory' })
    expect(opened.open).toHaveBeenCalledWith(expect.objectContaining({ hubUrl: 'http://hub', adminToken: 'token', workerId: 'worker-1' }), 'workspace-1', '/run/native-code-execution-directory', agentOpenOptions(AgentProvider.CURSOR))
    expect(opened.events).toEqual(['directory native-code-execution-', 'open /run/native-code-execution-directory', 'workspace workspace-1', 'selected tab'])
  })

  it('creates the new directory by the working directory rule of the provider, with the stated prefix', async () => {
    const { agent, workingDir } = repositoryAgent()
    expect(await openNativeAgent(contextOf(agent), { directoryPrefix: 'native-code-execution-' })).toEqual({ agentId: 'agent-1', workingDir: '/run/native-code-execution-repository/repo' })
    expect(workingDir).toHaveBeenCalledExactlyOnceWith('native-code-execution-')
    expect(opened.events).toEqual(['repository native-code-execution-', 'open /run/native-code-execution-repository/repo', 'workspace workspace-1', 'selected tab'])
  })

  it('gives the rule of the provider the default prefix when the caller states none', async () => {
    const { agent, workingDir } = repositoryAgent()
    await openNativeAgent(contextOf(agent))
    expect(workingDir).toHaveBeenCalledExactlyOnceWith('native-agent-')
  })

  it('refuses the agent of another provider before it creates a directory or opens an agent', async () => {
    const { agent, workingDir } = repositoryAgent()
    await expect(openNativeAgent(contextOf({ ...agent, provider: AgentProvider.CLINE }))).rejects.toThrow(`A native agent of provider ${AgentProvider.CURSOR} opens by its own rule, not by the rule of provider ${AgentProvider.CLINE}.`)
    expect(workingDir).not.toHaveBeenCalled()
    expect(opened.events).toEqual([])
    expect(opened.open).not.toHaveBeenCalled()
  })

  it('creates no directory and opens no agent when the merge rule refuses an override', async () => {
    const { agent, workingDir } = repositoryAgent()
    await expect(openNativeAgent(contextOf(agent), { overrides: { model: ' ' } })).rejects.toThrow('a model override needs a model ID')
    expect(workingDir).not.toHaveBeenCalled()
    expect(opened.events).toEqual([])
    expect(opened.open).not.toHaveBeenCalled()
  })

  it('shows no workspace when the agent does not open', async () => {
    opened.open.mockRejectedValue(new Error('the Worker refused the agent'))
    await expect(openNativeAgent(context)).rejects.toThrow('the Worker refused the agent')
    expect(opened.events).toEqual(['directory native-agent-'])
  })

  it('opens the agent in an existing directory, and neither the rule of the provider nor the run creates one', async () => {
    const { agent, workingDir } = repositoryAgent()
    expect(await openNativeAgent(contextOf(agent), { workingDir: unitWorkingDir('/run/existing') })).toEqual({ agentId: 'agent-1', workingDir: '/run/existing' })
    expect(workingDir).not.toHaveBeenCalled()
    expect(opened.events).toEqual(['open /run/existing', 'workspace workspace-1', 'selected tab'])
  })

  it('refuses an existing directory and a prefix together, before it opens an agent', async () => {
    await expect(openNativeAgent(context, { workingDir: unitWorkingDir('/run/existing'), directoryPrefix: 'native-' })).rejects.toThrow('not in both')
    expect(opened.events).toEqual([])
    expect(opened.open).not.toHaveBeenCalled()
  })

  it('fails when an earlier agent stays the selected tab', async () => {
    opened.selected = 'agent-0'
    await expect(openNativeAgent(context)).rejects.toThrow('the new native agent is the selected tab')
  })
})

describe('requireOwnProviderAgent', () => {
  it('accepts the agent of the provider of the context', () => {
    expect(requireOwnProviderAgent(context)).toBe(cursor)
  })

  it('refuses the agent of another provider', () => {
    expect(() => requireOwnProviderAgent(contextOf({ provider: AgentProvider.QODER, prefix: 'qoder-e2e' }))).toThrow(`not by the rule of provider ${AgentProvider.QODER}`)
  })

  it('refuses a context that states no agent, as a context built by hand can', () => {
    const handBuilt = { ...context, providerAgent: undefined } as unknown as ManagedNativeScenarioContext
    expect(() => requireOwnProviderAgent(handBuilt)).toThrow(`A native agent of provider ${AgentProvider.CURSOR} opens by its own rule, and the scenario context states no rule.`)
  })
})

describe('newNativeWorkingDir', () => {
  it('creates a fresh directory of the run with the stated prefix for a provider with no rule', () => {
    expect(newNativeWorkingDir(context, 'native-resume-keeper-')).toBe('/run/native-resume-keeper-directory')
    expect(opened.events).toEqual(['directory native-resume-keeper-'])
  })

  it('creates the directory by the rule of the provider of the context, with the stated prefix', () => {
    const { agent, workingDir } = repositoryAgent()
    expect(newNativeWorkingDir(contextOf(agent), 'native-resume-keeper-')).toBe('/run/native-resume-keeper-repository/repo')
    expect(workingDir).toHaveBeenCalledExactlyOnceWith('native-resume-keeper-')
    expect(opened.events).toEqual(['repository native-resume-keeper-'])
  })

  it('refuses the agent of another provider before it creates a directory', () => {
    const { agent, workingDir } = repositoryAgent()
    expect(() => newNativeWorkingDir(contextOf({ ...agent, provider: AgentProvider.KIRO }), 'native-')).toThrow(`not by the rule of provider ${AgentProvider.KIRO}`)
    expect(workingDir).not.toHaveBeenCalled()
    expect(opened.events).toEqual([])
  })
})

describe('exerciseProviderAgentButtons', () => {
  function buttonContext(): ManagedNativeScenarioContext {
    const button = {
      filter: () => button,
      first: () => button,
      click: async () => { buttons.stage = 2 },
    }
    buttons.button = button
    const page = {
      getByTestId: (id: string) => id === 'tab-more-menu' ? { click: async () => {} } : button,
      locator: () => ({ click: async () => { buttons.stage = 1 } }),
    } as unknown as Page
    return { ...context, page }
  }

  it('requires separate selected agents from the menu and button', async () => {
    await exerciseProviderAgentButtons(buttonContext())
    expect(buttons.counts).toEqual([2, 3])
    expect(buttons.stage).toBe(2)
  })

  it('rejects the original selected agent after menu creation', async () => {
    buttons.ids[1] = 'original'
    await expect(exerciseProviderAgentButtons(buttonContext())).rejects.toThrow('the menu selects a new Worker agent')
    expect(buttons.stage).toBe(1)
  })

  it.each(['original', 'from-menu'])('rejects an existing selected agent after button creation: %s', async (id) => {
    buttons.ids[2] = id
    await expect(exerciseProviderAgentButtons(buttonContext())).rejects.toThrow('the button selects another new Worker agent')
    expect(buttons.counts).toEqual([2, 3])
  })

  it.each([1, 2])('rejects a selected agent from another provider at creation step %s', async (stage) => {
    buttons.providers[stage] = AgentProvider.CLINE
    await expect(exerciseProviderAgentButtons(buttonContext())).rejects.toThrow('the selected Worker agent uses the requested provider')
    expect(buttons.stage).toBe(stage)
  })

  it('refuses the wrong provider rule before either creation control runs', async () => {
    const native = buttonContext()
    native.providerAgent = { provider: AgentProvider.CLINE, prefix: 'wrong-provider' }
    await expect(exerciseProviderAgentButtons(native)).rejects.toThrow('not by the rule of provider')
    expect(buttons.stage).toBe(0)
    expect(buttons.counts).toEqual([])
  })
})
