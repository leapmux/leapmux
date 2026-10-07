import type { Locator, Page } from '@playwright/test'
import type { MockModelRequestRecord, MockModelStep } from './mockModelScript'
import type { ModelScript } from './modelScriptFixture'
import type { NativeScenarioContext } from './nativeScenario'
import type { QuestionRequest } from './providerToolCalls'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { ASKED_QUESTIONS_ANSWER, ASKED_QUESTIONS_CALL_ID, askQuestions, chooseQuestionOption, exerciseQuestionAnswer, pickQuestionOption } from './nativeQuestion'
import { askUserQuestionToolCall } from './providerToolCalls'

/** The browser steps that the helper takes, in order. */
const browser = vi.hoisted(() => ({ events: [] as string[] }))

/** A fake locator that records the assertions and the clicks on it. */
interface FakeLocator {
  fake: string
}

function fakeLocator(name: string): FakeLocator {
  return { fake: name }
}

vi.mock('./ui', () => ({
  sendMessage: async (_page: Page, text: string) => {
    browser.events.push(`send:${text}`)
  },
  waitForAgentIdle: async () => {
    browser.events.push('idle')
  },
  waitForControlBanner: async () => {
    browser.events.push('banner')
    return fakeLocator('banner')
  },
  expectNoControlBanner: async () => {
    browser.events.push('no-banner')
  },
  assistantBubbles: () => ({ filter: ({ hasText }: { hasText: string }) => ({ first: () => fakeLocator(`answer:${hasText}`) }) }),
  controlButton: (_page: Page, action: string) => ({
    click: async () => {
      browser.events.push(`click:${action}`)
    },
  }),
}))

vi.mock('@playwright/test', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@playwright/test')>()
  return {
    ...actual,
    expect: (value: unknown) => {
      if (typeof value === 'object' && value !== null && 'fake' in value && typeof value.fake === 'string') {
        const name = value.fake
        return {
          toContainText: async (text: string) => {
            browser.events.push(`${name} holds:${text}`)
          },
          toBeVisible: async () => {
            browser.events.push(`${name} visible`)
          },
        }
      }
      return expect(value)
    },
  }
})

const COLOR: QuestionRequest = {
  question: 'Which color should I use?',
  header: 'Color',
  options: [{ label: 'Blue', description: 'Use blue.' }, { label: 'Green', description: 'Use green.' }],
}

/** A Chat Completions request that holds one tool result for `callId`. */
function toolResultRequest(stepIndex: number, callId: string, content: string): MockModelRequestRecord {
  return {
    stepIndex,
    protocol: 'openai-chat-completions',
    path: '/v1/chat/completions',
    body: { messages: [{ role: 'user', content: 'Ask.' }, { role: 'tool', tool_call_id: callId, content }] },
  }
}

/** A script whose queue starts at `offset`, as after an earlier turn of the same test. */
function fakeScript(offset: number, records: Record<number, MockModelRequestRecord>) {
  const queued: MockModelStep[] = []
  const fallbacks: MockModelStep[] = []
  const script = {
    queued,
    fallbacks,
    prompt: (text: string) => `MARKED:${text}`,
    queue: vi.fn(async (...steps: MockModelStep[]) => {
      const index = offset + queued.length
      queued.push(...steps)
      return index
    }),
    fallback: vi.fn(async (step: MockModelStep) => {
      browser.events.push('fallback')
      fallbacks.push(step)
    }),
    status: vi.fn(async () => ({ requests: Object.values(records) })),
    waitForSteps: vi.fn(async (count?: number) => {
      browser.events.push(`steps:${count ?? 'all'}`)
    }),
    waitForGate: vi.fn(async (gate: string) => {
      browser.events.push(`gate:${gate}`)
    }),
    requestAt: vi.fn(async (stepIndex: number) => {
      browser.events.push(`request:${stepIndex}`)
      const record = records[stepIndex]
      if (!record)
        throw new Error(`The fake script holds no request for step ${stepIndex}.`)
      return record
    }),
  }
  return { script, queued }
}

function context(script: ReturnType<typeof fakeScript>['script'], extra: Partial<NativeScenarioContext> = {}): NativeScenarioContext {
  return { page: {} as Page, modelScript: script as unknown as ModelScript, provider: AgentProvider.GITHUB_COPILOT, ...extra }
}

beforeEach(() => {
  browser.events.length = 0
})

describe('exerciseQuestionAnswer', () => {
  it('runs one question turn from the queue index and returns the reply of the question call', async () => {
    const { script, queued } = fakeScript(3, { 4: toolResultRequest(4, 'color-question', 'The user chose Green.') })
    const reply = vi.fn(async (banner: Locator) => {
      browser.events.push(`reply:${(banner as unknown as FakeLocator).fake}`)
    })
    const turn = await exerciseQuestionAnswer(context(script), { questions: [COLOR], reply, callId: 'color-question', answer: 'I used the chosen color.', prompt: 'Ask me.' })
    expect(queued).toEqual([
      { toolCalls: [askUserQuestionToolCall(AgentProvider.GITHUB_COPILOT, 'color-question', [COLOR])] },
      { text: 'I used the chosen color.' },
    ])
    expect(browser.events).toEqual([
      'send:MARKED:Ask me.',
      'steps:4',
      'banner',
      `banner holds:${COLOR.question}`,
      'reply:banner',
      'steps:5',
      'idle',
      'request:4',
      'answer:I used the chosen color. visible',
      'no-banner',
    ])
    expect(turn).toEqual({ result: 'The user chose Green.', request: toolResultRequest(4, 'color-question', 'The user chose Green.') })
  })

  it('reads the reply through the tool result reader of the context', async () => {
    const { script } = fakeScript(0, { 1: toolResultRequest(1, 'native-question', 'unused') })
    const readToolResult = vi.fn(async () => ({ text: 'The provider reader read Blue.' }))
    const turn = await exerciseQuestionAnswer(context(script, { readToolResult }), { questions: [COLOR], reply: async () => {} })
    expect(readToolResult).toHaveBeenCalledWith(turn.request, 'native-question')
    expect(turn.result).toBe('The provider reader read Blue.')
  })

  it('reads the reply through the reader of the call when the caller gives one', async () => {
    const { script } = fakeScript(0, { 1: toolResultRequest(1, 'droid-question', 'unused') })
    const readToolResult = vi.fn(async () => ({ text: 'The context reader must not run.' }))
    const readResult = vi.fn(async (_request: MockModelRequestRecord, callId: string) => `The call reader read ${callId}.`)
    const turn = await exerciseQuestionAnswer(context(script, { readToolResult }), { questions: [COLOR], reply: async () => {}, callId: 'droid-question', readResult })
    expect(readToolResult).not.toHaveBeenCalled()
    expect(readResult).toHaveBeenCalledWith(turn.request, 'droid-question')
    expect(turn.result).toBe('The call reader read droid-question.')
  })

  it('answers through the text step of a provider that answers with a tool', async () => {
    const { script, queued } = fakeScript(0, { 1: toolResultRequest(1, 'native-question', 'Red') })
    const answerStep = { toolCalls: [{ id: 'native-answer', name: 'answer', arguments: { text: 'The provider answered.' } }] }
    await exerciseQuestionAnswer(context(script, { textStep: () => answerStep }), { questions: [COLOR], reply: async () => {}, answer: 'The provider answered.' })
    expect(queued[1]).toEqual(answerStep)
  })

  it('stops at a failed reply and reads no request', async () => {
    const { script } = fakeScript(0, { 1: toolResultRequest(1, 'native-question', 'Red') })
    const failure = new Error('The reply found no option.')
    await expect(exerciseQuestionAnswer(context(script), { questions: [COLOR], reply: async () => {
      throw failure
    } })).rejects.toBe(failure)
    expect(script.requestAt).not.toHaveBeenCalled()
    expect(browser.events).not.toContain('idle')
  })

  it('refuses a turn with no question before it queues or sends anything', async () => {
    const { script } = fakeScript(0, {})
    await expect(exerciseQuestionAnswer(context(script), { questions: [], reply: async () => {} })).rejects.toThrow('at least one question')
    expect(script.queue).not.toHaveBeenCalled()
    expect(browser.events).toEqual([])
  })
})

/** A banner whose options record that they are visible and clicked. */
function questionBanner() {
  const getByTestId = vi.fn((testId: string) => ({
    fake: testId,
    click: vi.fn(async () => {
      browser.events.push(`click:${testId}`)
    }),
  }))
  return { banner: { getByTestId, page: () => ({}) } as unknown as Locator, getByTestId }
}

describe('pickQuestionOption', () => {
  it('requires the option inside the banner to be visible, clicks it, and submits nothing', async () => {
    const { banner, getByTestId } = questionBanner()
    await pickQuestionOption(banner, 'Tea')
    expect(getByTestId).toHaveBeenCalledExactlyOnceWith('question-option-Tea')
    expect(browser.events).toEqual(['question-option-Tea visible', 'click:question-option-Tea'])
  })

  it('refuses an empty label before it reads the banner', async () => {
    const { banner, getByTestId } = questionBanner()
    await expect(pickQuestionOption(banner, '')).rejects.toThrow('needs a label')
    expect(getByTestId).not.toHaveBeenCalled()
  })
})

describe('askQuestions', () => {
  it('queues the native question call after a fallback answer, sends the turn, and waits until the call arrives', async () => {
    const { script, queued } = fakeScript(2, { 0: toolResultRequest(0, 'earlier', 'one'), 1: toolResultRequest(1, 'earlier', 'two') })
    const before = await askQuestions(context(script), [COLOR])
    expect(before).toBe(2)
    expect(queued).toEqual([{ toolCalls: [askUserQuestionToolCall(AgentProvider.GITHUB_COPILOT, ASKED_QUESTIONS_CALL_ID, [COLOR])] }])
    expect(script.fallbacks).toEqual([{ text: ASKED_QUESTIONS_ANSWER }])
    expect(browser.events).toEqual(['fallback', 'send:MARKED:Ask the scripted questions and tell me what I answered.', 'steps:all'])
    expect(script.waitForGate).not.toHaveBeenCalled()
  })

  it('holds the question call at the gate and waits for the gate, not for the steps', async () => {
    const { script, queued } = fakeScript(0, {})
    await askQuestions(context(script), [COLOR], { gate: 'question-gate' })
    expect(queued).toEqual([{
      toolCalls: [askUserQuestionToolCall(AgentProvider.GITHUB_COPILOT, ASKED_QUESTIONS_CALL_ID, [COLOR])],
      gate: 'question-gate',
    }])
    expect(browser.events.at(-1)).toBe('gate:question-gate')
    expect(script.waitForSteps).not.toHaveBeenCalled()
  })

  it('answers each later request through the text step of a provider that answers with a tool', async () => {
    const { script } = fakeScript(0, {})
    const answerStep = { toolCalls: [{ id: 'native-answer', name: 'answer', arguments: { text: ASKED_QUESTIONS_ANSWER } }] }
    const textStep = vi.fn(() => answerStep)
    await askQuestions(context(script, { textStep }), [COLOR])
    expect(textStep).toHaveBeenCalledExactlyOnceWith(ASKED_QUESTIONS_ANSWER)
    expect(script.fallbacks).toEqual([answerStep])
  })

  it('refuses a call with no question before it reads or changes the script', async () => {
    const { script } = fakeScript(0, {})
    await expect(askQuestions(context(script), [])).rejects.toThrow('at least one question')
    expect(script.status).not.toHaveBeenCalled()
    expect(script.fallback).not.toHaveBeenCalled()
    expect(script.queue).not.toHaveBeenCalled()
    expect(browser.events).toEqual([])
  })
})

describe('chooseQuestionOption', () => {
  it('picks the option inside the banner, then sends the answer through the visible Submit', async () => {
    const { banner, getByTestId } = questionBanner()
    await chooseQuestionOption('Green')(banner)
    expect(getByTestId).toHaveBeenCalledWith('question-option-Green')
    expect(browser.events).toEqual(['question-option-Green visible', 'click:question-option-Green', 'click:submit'])
  })

  it('refuses an empty label', () => {
    expect(() => chooseQuestionOption('')).toThrow('needs a label')
  })
})
