import type { Page } from '@playwright/test'
import type { SeparateServerInfo } from '../process-control-fixtures'
import type { ModelScript } from './modelScriptFixture'
import type { ManagedNativeScenarioContext } from './nativeScenario'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { exerciseWorkerRestart, expectAnswerAndTurnEnd, waitForWorkerConnection, withRestartWorkspace } from './nativeWorkerRestart'

/** The steps of one scenario in order. */
const steps = vi.hoisted(() => ({
  events: [] as string[],
  connected: 0,
  opened: [] as unknown[],
  turns: [] as { role: string, content: string }[],
  restoredSession: 'native-session',
  retainContext: true,
  restartFailure: undefined as Error | undefined,
}))

vi.mock('../process-control-fixtures', () => ({
  ensureWorkerOnline: async () => { steps.events.push('worker-online') },
  stopWorker: async () => {
    steps.events.push('stop-worker')
    steps.connected = 0
  },
  waitForWorkerOffline: async () => { steps.events.push('worker-offline') },
  restartWorker: async () => {
    steps.events.push('restart-worker')
    if (steps.restartFailure)
      throw steps.restartFailure
    steps.connected = 1
  },
  stopHub: async () => { steps.events.push('stop-hub') },
  restartHub: async () => { steps.events.push('restart-hub') },
}))

vi.mock('./retryUntilPass', () => ({
  retryUntilPass: async (operation: () => Promise<unknown>) => operation(),
}))

vi.mock('./nativeScenario', async (importOriginal) => {
  const actual = await importOriginal<typeof import('./nativeScenario')>()
  const { AgentStatus } = await import('../../../src/generated/proto/leapmux/v1/agent_pb')
  return {
    ...actual,
    currentNativeAgent: async () => ({ id: 'restart-agent', agentProvider: AgentProvider.CODEX, agentSessionId: 'native-session' }),
    nativeAgentById: async () => ({
      id: 'restart-agent',
      agentProvider: AgentProvider.CODEX,
      agentSessionId: steps.restoredSession,
      status: AgentStatus.ACTIVE,
    }),
  }
})

vi.mock('./nativeConversation', () => ({
  sendNativeAnswer: async (_context: unknown, prompt: string, answer: string) => {
    steps.events.push('native-turn')
    if (!steps.retainContext)
      steps.turns = []
    steps.turns.push({ role: 'user', content: prompt })
    const request = {
      protocol: 'openai-chat-completions',
      path: '/v1/chat/completions',
      body: {
        messages: [...steps.turns],
      },
    }
    steps.turns.push({ role: 'assistant', content: answer })
    return request
  },
}))

vi.mock('./nativeResume', async importOriginal => ({
  ...await importOriginal<typeof import('./nativeResume')>(),
  countOriginalAnswerRows: async () => 1,
  expectResumedConversation: async (_context: unknown, agentId: string, _texts: unknown, rows: number, bubbles: number) => {
    expect({ agentId, rows, bubbles }).toEqual({ agentId: 'restart-agent', rows: 1, bubbles: 1 })
    steps.events.push('transcript')
  },
}))

vi.mock('./workspace', () => ({
  openProviderAgent: async (_server: unknown, workspaceId: string, provider: unknown, options?: { optionValues?: { permissionMode?: string } }) => {
    steps.opened.push({ provider, options })
    const pinned = options?.optionValues?.permissionMode === 'default'
    steps.events.push(`${pinned ? 'open-pinned-agent' : 'open-agent'}:${workspaceId}`)
    return {
      agentId: pinned ? 'pinned-agent' : 'restart-agent',
      workingDir: '/private/restart',
    }
  },
  withTestWorkspace: async (_server: unknown, prefix: string, use: (workspace: { workspaceId: string }) => Promise<void>) => {
    steps.events.push(`workspace:${prefix}`)
    try {
      await use({ workspaceId: 'restart-workspace' })
    }
    finally {
      steps.events.push('workspace-deleted')
    }
  },
}))

vi.mock('./ui', () => ({
  loginViaToken: async (_page: Page, token: string) => { steps.events.push(`login:${token}`) },
  openWorkspace: async (_page: Page, workspaceId: string) => { steps.events.push(`show:${workspaceId}`) },
  expectAssistantAnswer: async () => { steps.events.push('answer') },
  waitForAgentIdle: async () => { steps.events.push('idle') },
  reopenWorkspace: async () => { steps.events.push('reopen-workspace') },
  assistantBubbles: () => ({
    filter: () => ({
      count: async () => 1,
    }),
  }),
}))

vi.mock('@playwright/test', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@playwright/test')>()
  const check = (value: unknown, message?: string) => {
    if (typeof value === 'object' && value !== null && 'statusProbe' in value) {
      return {
        toHaveCount: async (count: number) => {
          steps.events.push(`count:${count}`)
          expect(steps.connected).toBe(count)
        },
        not: {
          toHaveCount: async (count: number) => {
            steps.events.push(`not-count:${count}`)
            expect(steps.connected).not.toBe(count)
          },
        },
      }
    }
    return expect(value, message)
  }
  return { ...actual, expect: check }
})

const provider = { provider: AgentProvider.CODEX, prefix: 'restart-test' }

const server = {
  hubUrl: 'http://unused.invalid',
  adminToken: 'restart-token',
  workerId: 'restart-worker',
  hubProc: { exitCode: null, signalCode: null },
} as SeparateServerInfo

const page = Object.assign({} as Page, {
  reload: async () => { steps.events.push('reload') },
  getByTestId: (id: string) => ({
    locator: (selector: string) => {
      steps.events.push(`locate:${id} ${selector}`)
      return { statusProbe: true }
    },
  }),
})

beforeEach(() => {
  steps.events = []
  steps.connected = 0
  steps.opened = []
  steps.turns = []
  steps.restoredSession = 'native-session'
  steps.retainContext = true
  steps.restartFailure = undefined
})

describe('exerciseWorkerRestart', () => {
  const context = {
    page,
    leapmuxServer: server,
    provider: AgentProvider.CODEX,
    providerAgent: provider,
    modelScript: {} as ModelScript,
    workspaceId: 'restart-workspace',
  } satisfies ManagedNativeScenarioContext

  it('requires the native checkpoint before stopping the Worker', async () => {
    const checkpoint = vi.fn(async () => {
      steps.events.push('checkpoint')
    })
    await exerciseWorkerRestart({ ...context, waitForNativeCheckpoint: checkpoint }, server, 'worker')
    expect(checkpoint).toHaveBeenCalledExactlyOnceWith('native-session')
    expect(steps.events.indexOf('checkpoint')).toBeLessThan(steps.events.indexOf('stop-worker'))
  })

  it('keeps the Worker running when the native checkpoint fails', async () => {
    const failure = new Error('The native checkpoint did not reach disk.')
    await expect(exerciseWorkerRestart({
      ...context,
      waitForNativeCheckpoint: async () => {
        throw failure
      },
    }, server, 'worker')).rejects.toBe(failure)
    expect(steps.events).not.toContain('stop-worker')
    expect(steps.events).not.toContain('restart-worker')
  })

  it.each(['worker', 'hub-and-worker'] as const)('retains the actual conversation and checks its transcript after a %s restart', async (restart) => {
    await exerciseWorkerRestart(context, server, restart)
    expect(steps.events.filter(event => event === 'native-turn')).toHaveLength(2)
    expect(steps.events.filter(event => event === 'transcript')).toHaveLength(2)
    expect(steps.events.indexOf('worker-offline')).toBeGreaterThan(steps.events.indexOf('stop-worker'))
    expect(steps.events.indexOf('restart-worker')).toBeGreaterThan(steps.events.indexOf('worker-offline'))
    expect(steps.events.at(-1)).toBe('worker-online')
    if (restart === 'hub-and-worker') {
      expect(steps.events.indexOf('restart-hub')).toBeGreaterThan(steps.events.indexOf('stop-hub'))
      expect(steps.events).toContain('reopen-workspace')
    }
    else {
      expect(steps.events).not.toContain('stop-hub')
    }
  })

  it('rejects a replacement native session and restores the Worker before reporting failure', async () => {
    steps.restoredSession = 'different-session'
    await expect(exerciseWorkerRestart(context, server, 'worker')).rejects.toThrow('the restart retains the native session')
    expect(steps.events.filter(event => event === 'native-turn')).toHaveLength(1)
    expect(steps.events.at(-1)).toBe('worker-online')
  })

  it('rejects a resumed request that lost the original conversation', async () => {
    steps.retainContext = false
    await expect(exerciseWorkerRestart(context, server, 'worker')).rejects.toThrow('The native request must hold the original prompt in a user turn.')
    expect(steps.events).not.toContain('transcript')
    expect(steps.events.at(-1)).toBe('worker-online')
  })

  it('preserves a restart failure and still attempts Worker recovery', async () => {
    const failure = new Error('The Worker could not restart.')
    steps.restartFailure = failure
    await expect(exerciseWorkerRestart(context, server, 'worker')).rejects.toBe(failure)
    expect(steps.events.at(-1)).toBe('worker-online')
  })
})

describe('withRestartWorkspace', () => {
  it('confirms the Worker, opens the agent, shows the workspace, and runs the operation inside the workspace scope', async () => {
    const seen: unknown[] = []
    await withRestartWorkspace(page, server, { provider, prefix: 'Restart Test' }, async (workspace) => {
      seen.push(workspace)
      steps.events.push('operation')
    })
    expect(seen).toEqual([
      { workspaceId: 'restart-workspace', agentId: 'restart-agent' },
    ])
    expect(steps.opened).toEqual([
      { provider, options: undefined },
    ])
    expect(steps.events).toEqual([
      'worker-online',
      'workspace:Restart Test',
      'open-agent:restart-workspace',
      'login:restart-token',
      'show:restart-workspace',
      'operation',
      'workspace-deleted',
    ])
  })

  it('opens the agent in the Default permission mode when the caller asks', async () => {
    const seen: unknown[] = []
    await withRestartWorkspace(page, server, {
      provider,
      prefix: 'Pinned',
      openOptions: {
        optionValues: { permissionMode: 'default' },
      },
    }, async (workspace) => {
      seen.push(workspace)
    })
    expect(seen).toEqual([
      { workspaceId: 'restart-workspace', agentId: 'pinned-agent' },
    ])
    expect(steps.opened).toEqual([
      {
        provider,
        options: {
          optionValues: { permissionMode: 'default' },
        },
      },
    ])
    expect(steps.events).toContain('open-pinned-agent:restart-workspace')
    expect(steps.events).not.toContain('open-agent:restart-workspace')
  })

  it('keeps the failure of the operation, and still leaves the workspace scope', async () => {
    const failure = new Error('The restart scenario failed.')
    await expect(withRestartWorkspace(page, server, { provider, prefix: 'Failing' }, async () => {
      throw failure
    })).rejects.toBe(failure)
    expect(steps.events.at(-1)).toBe('workspace-deleted')
  })
})

describe('expectAnswerAndTurnEnd', () => {
  it('waits for the answer and then for the end of the turn', async () => {
    await expectAnswerAndTurnEnd(page)
    expect(steps.events).toEqual(['answer', 'idle'])
  })
})

describe('waitForWorkerConnection', () => {
  it('waits for a connected Worker status in the Workers section', async () => {
    steps.connected = 1
    await waitForWorkerConnection(page, true)
    expect(steps.events).toEqual(['locate:section-header-workers [data-status="connected"]', 'not-count:0'])
  })

  it('waits until no Worker status states a connection', async () => {
    await waitForWorkerConnection(page, false)
    expect(steps.events).toEqual(['locate:section-header-workers [data-status="connected"]', 'count:0'])
  })

  it('fails while the browser still shows the Worker as connected', async () => {
    steps.connected = 1
    // The count of connected Worker statuses fails: one where the wait requires none.
    await expect(waitForWorkerConnection(page, false)).rejects.toThrow('expected 1 to be +0')
  })
})
