import type { Page } from '@playwright/test'
import type { MockModelRequestRecord, MockModelStep } from './mockModelScript'
import type { ModelScript } from './modelScriptFixture'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { rateLimitPopoverLabel } from '../../../src/lib/rateLimitUtils'
import { exerciseRateLimitWindow, NEAR_LIMIT_UTILIZATION, nearLimitRateLimits, rateLimitMarkers, rateLimitWindowLabel } from './rateLimit'

/** The browser and script events of one scenario, in order. */
const events = vi.hoisted(() => [] as string[])

vi.mock('./ui', () => ({
  sendMessage: async (_page: Page, prompt: string) => {
    events.push(`send ${prompt}`)
  },
  waitForAgentIdle: async () => {
    events.push('idle')
  },
  assistantBubbles: () => ({ filter: ({ hasText }: { hasText: string }) => ({ first: () => ({ fakeBubble: hasText }) }) }),
  openAgentInfoCard: async () => {
    events.push('card')
    return { fakeCard: true }
  },
  visibleOnly: (locator: unknown) => locator,
}))

vi.mock('@playwright/test', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@playwright/test')>()
  return {
    ...actual,
    expect: (value: unknown) => {
      if (typeof value === 'object' && value !== null && 'fakeBubble' in value)
        return { toBeVisible: async () => events.push('answer visible') }
      if (typeof value === 'object' && value !== null && 'fakeCard' in value)
        return { toContainText: async (text: string) => events.push(`card shows ${text}`) }
      return expect(value)
    },
  }
})

beforeEach(() => {
  events.length = 0
  vi.useFakeTimers({ now: new Date('2026-10-06T00:00:00Z'), toFake: ['Date'] })
})

afterEach(() => vi.useRealTimers())

/** A script that holds two earlier steps, and records what the scenario queues and reads. */
function fakeScript(): { script: ModelScript, queued: MockModelStep[] } {
  const queued: MockModelStep[] = []
  const script = {
    queue: async (...steps: MockModelStep[]) => {
      queued.push(...steps)
      events.push('queue')
      return 2
    },
    prompt: (text: string) => `MARKED ${text}`,
    waitForSteps: async (count: number) => {
      events.push(`wait ${count}`)
    },
    requestAt: async (stepIndex: number) => {
      events.push(`request ${stepIndex}`)
      return { stepIndex, response: { status: 200, headers: {} } } as unknown as MockModelRequestRecord
    },
  } as unknown as ModelScript
  return { script, queued }
}

describe('nearLimitRateLimits', () => {
  it('states a five-hour warning near the limit that resets in one hour', () => {
    expect(nearLimitRateLimits()).toEqual({
      type: 'five_hour',
      status: 'allowed_warning',
      utilization: NEAR_LIMIT_UTILIZATION,
      resetsAt: Date.parse('2026-10-06T01:00:00Z') / 1000,
    })
  })

  it('keeps the window type of the caller', () => {
    expect(nearLimitRateLimits('premium_interactions').type).toBe('premium_interactions')
  })

  it.each(['', ' '])('refuses an empty window type: %j', (type) => {
    expect(() => nearLimitRateLimits(type)).toThrow('needs a type')
  })
})

describe('exerciseRateLimitWindow', () => {
  const rateLimits = nearLimitRateLimits()

  function context(script: ModelScript, reload = vi.fn(async () => {
    events.push('reload')
  })) {
    return { page: { reload } as unknown as Page, modelScript: script, provider: AgentProvider.CODEX }
  }

  it('queues the window, waits for its own step and the idle turn, and reads the card', async () => {
    const { script, queued } = fakeScript()
    const record = await exerciseRateLimitWindow(context(script), rateLimits)
    expect(record.stepIndex).toBe(2)
    expect(queued).toHaveLength(1)
    expect(queued[0]?.rateLimits).toEqual(rateLimits)
    expect(queued[0]?.text).toMatch(/^RATELIMITWINDOW/)
    expect(events).toEqual([
      'queue',
      'send MARKED Reply once near the scripted rate limit.',
      'wait 3',
      'idle',
      'request 2',
      'answer visible',
      'card',
      'card shows 5-Hour Rate Limit',
      'card shows 92% used',
    ])
  })

  it('reads the card again after a reload', async () => {
    const { script } = fakeScript()
    await exerciseRateLimitWindow(context(script), rateLimits, { reload: true })
    expect(events.slice(-4)).toEqual(['reload', 'card', 'card shows 5-Hour Rate Limit', 'card shows 92% used'])
  })

  it('answers through the native text step of the provider', async () => {
    const { script, queued } = fakeScript()
    await exerciseRateLimitWindow({ ...context(script), textStep: text => ({ toolCalls: [{ id: 'answer', name: 'answer', arguments: { text } }] }) }, rateLimits)
    expect(queued[0]?.text).toBeUndefined()
    expect(queued[0]?.toolCalls?.[0]?.name).toBe('answer')
    expect(queued[0]?.rateLimits).toEqual(rateLimits)
  })
})

describe('rateLimitWindowLabel', () => {
  it('reads the app label table for a window type the build knows', () => {
    expect(rateLimitWindowLabel('five_hour')).toBe(rateLimitPopoverLabel('five_hour'))
    expect(rateLimitWindowLabel('seven_day')).toBe(rateLimitPopoverLabel('seven_day'))
  })

  it('states the heading the card falls back to for a type the table does not carry', () => {
    expect(rateLimitWindowLabel('workspace_owner_credits_depleted'))
      .toBe('Rate Limit (workspace_owner_credits_depleted)')
  })

  it('states the bare heading when a window carries no type', () => {
    expect(rateLimitWindowLabel(undefined)).toBe('Rate Limit')
  })
})

describe('rateLimitMarkers', () => {
  // The marker block of the five-hour rate-limit specs: the heading and the
  // utilization the card prints as a whole-percent phrase.
  it('marks the window with its heading and its utilization phrase', () => {
    expect(rateLimitMarkers({
      type: 'five_hour',
      status: 'allowed_warning',
      utilization: 0.92,
      resetsAt: 1893456000,
    })).toEqual(['5-Hour Rate Limit', '92% used'])
  })

  it('marks the weekly window by its own heading', () => {
    expect(rateLimitMarkers({ type: 'seven_day', status: 'allowed_warning', utilization: 0.81 }))
      .toEqual(['7-Day Rate Limit', '81% used'])
  })

  it('skips the utilization the step did not script', () => {
    expect(rateLimitMarkers({ type: 'five_hour', status: 'allowed' }))
      .toEqual(['5-Hour Rate Limit'])
  })
})
