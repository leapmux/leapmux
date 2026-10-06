import type { Page } from '@playwright/test'
import type { SeparateServerInfo } from '../process-control-fixtures'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { expectAnswerAndTurnEnd, waitForWorkerConnection, withRestartWorkspace } from './workerRestart'

/** The steps of one scenario in order. */
const steps = vi.hoisted(() => ({ events: [] as string[], connected: 0 }))

vi.mock('../process-control-fixtures', () => ({
  ensureWorkerOnline: async () => { steps.events.push('worker-online') },
}))

vi.mock('../helpers/workspace', () => ({
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

vi.mock('../helpers/api', () => ({
  openAgentViaAPI: async (_server: unknown, workspaceId: string) => {
    steps.events.push(`open-agent:${workspaceId}`)
    return 'restart-agent'
  },
  openPinnedModeAgentViaAPI: async (_server: unknown, workspaceId: string) => {
    steps.events.push(`open-pinned-agent:${workspaceId}`)
    return 'pinned-agent'
  },
}))

vi.mock('../helpers/ui', () => ({
  loginViaToken: async (_page: Page, token: string) => { steps.events.push(`login:${token}`) },
  openWorkspace: async (_page: Page, workspaceId: string) => { steps.events.push(`show:${workspaceId}`) },
  expectAssistantAnswer: async () => { steps.events.push('answer') },
  waitForAgentIdle: async () => { steps.events.push('idle') },
}))

vi.mock('@playwright/test', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@playwright/test')>()
  const check = (value: unknown) => {
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
    return expect(value)
  }
  return { ...actual, expect: check }
})

const server = { hubUrl: 'http://unused.invalid', adminToken: 'restart-token', workerId: 'restart-worker' } as SeparateServerInfo

const page = Object.assign({} as Page, {
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
})

describe('withRestartWorkspace', () => {
  it('confirms the Worker, opens the agent, shows the workspace, and runs the operation inside the workspace scope', async () => {
    const seen: unknown[] = []
    await withRestartWorkspace(page, server, { prefix: 'Restart Test' }, async (workspace) => {
      seen.push(workspace)
      steps.events.push('operation')
    })
    expect(seen).toEqual([{ workspaceId: 'restart-workspace', agentId: 'restart-agent' }])
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
    await withRestartWorkspace(page, server, { prefix: 'Pinned', pinnedMode: true }, async (workspace) => {
      seen.push(workspace)
    })
    expect(seen).toEqual([{ workspaceId: 'restart-workspace', agentId: 'pinned-agent' }])
    expect(steps.events).toContain('open-pinned-agent:restart-workspace')
    expect(steps.events).not.toContain('open-agent:restart-workspace')
  })

  it('keeps the failure of the operation, and still leaves the workspace scope', async () => {
    const failure = new Error('The restart scenario failed.')
    await expect(withRestartWorkspace(page, server, { prefix: 'Failing' }, async () => {
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
    await expect(waitForWorkerConnection(page, false)).rejects.toThrow()
  })
})
