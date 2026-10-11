/**
 * The unit tests check the reasoning turn that the thinking scenario scripts, and the order of its row checks.
 * The thinking-in-the-transcript browser spec of each provider checks the actual rows.
 */
import type { Page } from '@playwright/test'
import type { MockModelRequestRecord, MockModelStep } from './mockModelScript'
import type { ModelScript } from './modelScriptFixture'
import type { NativeScenarioContext } from './nativeScenario'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { exerciseThinkingRows, thinkingTurn, thoughtRowTexts } from './thinkingRows'

/** The browser actions and checks that the fakes record, in order. */
const recorded = vi.hoisted(() => ({
  events: [] as string[],
  /** The start of the recorded text of a check that fails, or '' when every check passes. */
  failingPrefix: '',
}))

/** A fake locator states its selector chain, so each recorded check identifies the rows that it reads. */
const fakes = vi.hoisted(() => {
  interface FakeLocator {
    description: string
    filter: (options: { hasText: string }) => FakeLocator
    first: () => FakeLocator
  }
  function locator(description: string): FakeLocator {
    return {
      description,
      filter: ({ hasText }) => locator(`${description} with ${hasText}`),
      first: () => locator(`first ${description}`),
    }
  }
  return { locator }
})

vi.mock('./ui', () => ({
  bandRows: (_page: Page, kind?: string) => fakes.locator(`${kind ?? 'all'} rows`),
  expectRowsInOrder: async (rows: { description: string }, texts: readonly string[]) => {
    recorded.events.push(`order ${rows.description}: ${texts.join(' | ')}`)
  },
  sendMessage: async (_page: Page, text: string) => {
    recorded.events.push(`send ${text}`)
  },
  waitForAgentIdle: async () => {
    recorded.events.push('idle')
  },
}))

vi.mock('@playwright/test', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@playwright/test')>()
  const record = (event: string) => {
    if (recorded.failingPrefix !== '' && event.startsWith(recorded.failingPrefix))
      throw new Error(`The fake check failed: ${event}`)
    recorded.events.push(event)
  }
  return {
    ...actual,
    expect: (value: { description: string }) => ({
      toBeVisible: async () => record(`visible ${value.description}`),
      toHaveCount: async (count: number) => record(`count ${count} ${value.description}`),
    }),
  }
})

/** A context whose script starts at `start`, as after earlier turns, and whose records state their step. */
function fakeContext(start: number, textStep?: NativeScenarioContext['textStep']): NativeScenarioContext & { queued: MockModelStep[] } {
  const queued: MockModelStep[] = []
  const modelScript = {
    prompt: (text: string) => `${text} [marked]`,
    queue: async (...steps: MockModelStep[]) => {
      queued.push(...steps)
      recorded.events.push(`queue ${steps.length}`)
      return start
    },
    waitForSteps: async (count: number) => {
      recorded.events.push(`steps ${count}`)
    },
    requestAt: async (stepIndex: number): Promise<MockModelRequestRecord> => {
      recorded.events.push(`request ${stepIndex}`)
      return { protocol: 'openai-chat-completions', path: '/v1/chat/completions', stepIndex, body: {} }
    },
  } as unknown as ModelScript
  const page = {
    reload: async () => {
      recorded.events.push('reload')
      return null
    },
  } as unknown as Page
  return { page, modelScript, provider: AgentProvider.OPENCODE, queued, ...(textStep ? { textStep } : {}) }
}

beforeEach(() => {
  recorded.events.length = 0
  recorded.failingPrefix = ''
})

describe('thinkingTurn', () => {
  it('answers with plain text and the reasoning for a provider with no text step', () => {
    const turn = thinkingTurn(fakeContext(0), 'MARK')
    expect(turn.step).toEqual({ text: turn.answer, reasoning: turn.reasoning })
  })

  it('keeps the answer tool of a provider text step and adds the reasoning to that step', () => {
    const respond = { id: 'respond', name: 'respond', arguments: {} }
    const turn = thinkingTurn(fakeContext(0, () => ({ toolCalls: [respond] })), 'MARK')
    expect(turn.step).toEqual({ toolCalls: [respond], reasoning: turn.reasoning })
  })

  it('gives each text the marker, and no text holds another', () => {
    const turn = thinkingTurn(fakeContext(0), 'MARK')
    const texts = [turn.prompt, turn.reasoning, turn.answer]
    for (const text of texts)
      expect(text).toContain('MARK')
    for (const text of texts)
      expect(texts.filter(other => other !== text && other.includes(text))).toEqual([])
  })

  it('gives each turn its own marker by default', () => {
    expect(thinkingTurn(fakeContext(0)).reasoning).not.toBe(thinkingTurn(fakeContext(0)).reasoning)
  })

  it('refuses an empty marker', () => {
    expect(() => thinkingTurn(fakeContext(0), '')).toThrow('needs a marker')
  })
})

describe('thoughtRowTexts', () => {
  const turn = { reasoning: 'the reasoning', answer: 'the answer' }

  it('puts the reasoning first for a thought before its answer', () => {
    expect(thoughtRowTexts(turn, 'before')).toEqual(['the reasoning', 'the answer'])
  })

  it('puts the answer first for a thought after its answer', () => {
    expect(thoughtRowTexts(turn, 'after')).toEqual(['the answer', 'the reasoning'])
  })
})

describe('exerciseThinkingRows', () => {
  /** The checks of one pass, for the texts of `turn`, in the order that the scenario runs them. */
  function rowChecks(turn: { reasoning: string, answer: string }, ordered: readonly string[]): string[] {
    return [
      `visible first thought rows with ${turn.reasoning}`,
      `visible first text rows with ${turn.answer}`,
      `count 0 text rows with ${turn.reasoning}`,
      `order all rows: ${ordered.join(' | ')}`,
    ]
  }

  it('scripts the turn from the queue index and checks the rows live and after reload', async () => {
    const context = fakeContext(3)
    const result = await exerciseThinkingRows(context)
    expect(context.queued).toEqual([{ text: result.answer, reasoning: result.reasoning }])
    const checks = rowChecks(result, [result.reasoning, result.answer])
    expect(recorded.events).toEqual([
      'queue 1',
      `send ${result.prompt} [marked]`,
      'steps 4',
      'idle',
      ...checks,
      'reload',
      ...checks,
      'request 3',
    ])
    expect(result.request.stepIndex).toBe(3)
  })

  it('requires the answer row before the thought row for the after order', async () => {
    const result = await exerciseThinkingRows(fakeContext(0), { order: 'after' })
    const orders = recorded.events.filter(event => event.startsWith('order '))
    expect(orders).toEqual([`order all rows: ${result.answer} | ${result.reasoning}`, `order all rows: ${result.answer} | ${result.reasoning}`])
  })

  it('scripts the answer through the text step of the provider', async () => {
    const respond = { id: 'respond', name: 'respond', arguments: {} }
    const context = fakeContext(0, () => ({ toolCalls: [respond] }))
    const result = await exerciseThinkingRows(context)
    expect(context.queued).toEqual([{ toolCalls: [respond], reasoning: result.reasoning }])
  })

  it('stops at a live text row that holds the reasoning, before the reload', async () => {
    // The marker of the turn is random, so the fake fails the check by the start of its text.
    recorded.failingPrefix = 'count 0 text rows with THINKINGREASONING'
    await expect(exerciseThinkingRows(fakeContext(0))).rejects.toThrow('The fake check failed: count 0 text rows with THINKINGREASONING')
    expect(recorded.events).not.toContain('reload')
    expect(recorded.events.filter(event => event.startsWith('order ') || event.startsWith('request '))).toEqual([])
  })
})
