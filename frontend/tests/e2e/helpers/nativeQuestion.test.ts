import type { Locator, Page } from '@playwright/test'
import type { MockModelRequestRecord, MockModelStep } from './mockModelScript'
import type { ModelScript } from './modelScriptFixture'
import type { ManagedNativeScenarioContext, NativeScenarioContext } from './nativeScenario'
import type { QuestionRequest } from './providerToolCalls'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { ASKED_QUESTIONS_ANSWER, ASKED_QUESTIONS_CALL_ID, askQuestions, chooseQuestionOption, chooseQuestionOptionWithNote, chooseQuestionPages, exerciseQuestionAnswer, exerciseQuestionCancellation, pickQuestionOption } from './nativeQuestion'
import { restoreQuestionSelection, restoreQuestionTextDraft } from './nativeQuestionRestoration'
import { askUserQuestionToolCall } from './providerToolCalls'

/** The browser steps that the helper takes, in order. */
const browser = vi.hoisted(() => ({
  events: [] as string[],
  restoredBanner: undefined as Locator | undefined,
  stored: { selections: { 0: ['native-green'] } } as unknown,
  restoredChecked: true,
  draft: 'Typed answer',
  mobileOwner: false,
  ownerId: 'question-agent',
}))

vi.mock('./nativeScenario', async importOriginal => ({
  ...await importOriginal<typeof import('./nativeScenario')>(),
  currentNativeAgent: async () => {
    if (browser.mobileOwner)
      throw new Error('The narrow layout hides the selected agent tab.')
    return { id: 'question-agent' }
  },
}))

vi.mock('./storage', () => ({
  waitForStoredEntry: async (_page: Page, prefix: string, accept: (value: unknown) => boolean) => {
    expect(prefix).toContain('question-agent:')
    expect(prefix).toContain('question-user')
    browser.events.push('stored')
    expect(accept(browser.stored), 'the stored selection matches the native value').toBe(true)
  },
}))

/** A fake locator that records the assertions and the clicks on it. */
interface FakeLocator {
  fake: string
}

function fakeLocator(name: string): FakeLocator {
  return { fake: name }
}

vi.mock('./ui', () => ({
  controlAgentId: async () => browser.ownerId,
  controlBanner: () => browser.restoredBanner ?? fakeLocator('banner'),
  sendMessage: async (_page: Page, text: string) => {
    browser.events.push(`send:${text}`)
  },
  waitForAgentIdle: async () => {
    browser.events.push('idle')
  },
  waitForControlBanner: async () => {
    browser.events.push('banner')
    return browser.restoredBanner ?? fakeLocator('banner')
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
  composerEditor: () => ({
    fake: 'composer',
    fill: async (text: string) => {
      browser.draft = text
      browser.events.push('fill-note')
    },
  }),
  waitForEditorDraft: async (_page: Page, userId: string, text: string) => {
    expect(userId).toBe('question-user')
    expect(text).toBe(browser.draft)
    browser.events.push('stored-draft')
  },
}))

vi.mock('@playwright/test', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@playwright/test')>()
  return {
    ...actual,
    expect: (value: unknown, message?: string) => {
      if (typeof value === 'object' && value !== null && 'fake' in value && typeof value.fake === 'string') {
        const name = value.fake
        return {
          toContainText: async (text: string) => {
            browser.events.push(`${name} holds:${text}`)
          },
          toBeVisible: async () => {
            browser.events.push(`${name} visible`)
          },
          toBeChecked: async () => {
            browser.events.push(`${name} checked`)
            expect(browser.restoredChecked, 'the restored option stays selected').toBe(true)
          },
          toHaveText: async (text: string) => {
            expect(browser.draft, 'the composer retains the question draft').toBe(text)
            browser.events.push(`${name} text`)
          },
        }
      }
      return expect(value, message)
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
    status: vi.fn(async () => ({ requests: Object.values(records), nextStep: offset + queued.length, unexpectedRequests: [] })),
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
  browser.restoredBanner = undefined
  browser.stored = { selections: { 0: ['native-green'] } }
  browser.restoredChecked = true
  browser.mobileOwner = false
  browser.ownerId = 'question-agent'
  browser.draft = 'Typed answer'
})

describe('exerciseQuestionCancellation', () => {
  it('queues only the native question and requires an ended turn without a later model request', async () => {
    const { script, queued } = fakeScript(3, {})
    await exerciseQuestionCancellation(context(script), {
      questions: [COLOR],
      reply: async () => {
        browser.events.push('cancel')
      },
    })
    expect(queued).toHaveLength(1)
    expect(script.fallbacks).toEqual([])
    expect(browser.events).toEqual([
      'send:MARKED:Ask the scripted question, then report the answer.',
      'steps:4',
      'banner',
      `banner holds:${COLOR.question}`,
      'cancel',
      'no-banner',
      'idle',
    ])
  })

  it('rejects a cancellation that starts another model request', async () => {
    const { script } = fakeScript(3, {})
    script.status.mockResolvedValue({ requests: [], nextStep: 5, unexpectedRequests: [] })
    await expect(exerciseQuestionCancellation(context(script), { questions: [COLOR], reply: async () => {} })).rejects.toThrow('the agent requested 4 ordered steps and no more')
  })
})

describe('restoreQuestionTextDraft', () => {
  function draftContext() {
    return {
      page: { reload: async () => { browser.events.push('reload') } },
      leapmuxServer: { adminUserId: 'question-user' },
    } as unknown as ManagedNativeScenarioContext
  }

  it('waits for durable storage before reload and checks the restored text', async () => {
    await restoreQuestionTextDraft(draftContext(), 'Typed answer')
    expect(browser.events).toEqual(['composer text', 'stored-draft', 'reload', 'banner', 'composer text'])
  })

  it('requires the active page to reach storage before restoring a typed answer', async () => {
    browser.stored = { currentPage: 0 }
    await restoreQuestionTextDraft(draftContext(), 'Typed answer', 0)
    expect(browser.events.indexOf('stored')).toBeLessThan(browser.events.indexOf('reload'))
    browser.events.length = 0
    await expect(restoreQuestionTextDraft(draftContext(), 'Typed answer', 1)).rejects.toThrow('the stored selection matches the native value')
    expect(browser.events).not.toContain('reload')
  })

  it.each([-1, 0.5, Number.NaN, Number.POSITIVE_INFINITY, Number.MAX_SAFE_INTEGER + 1])('rejects an invalid page before reading its draft: %s', async (index) => {
    await expect(restoreQuestionTextDraft(draftContext(), 'Typed answer', index)).rejects.toThrow('nonnegative safe integer')
    expect(browser.events).toEqual([])
  })

  it.each(['', ' \t\n'])('rejects an empty draft before changing the page: %j', async (text) => {
    await expect(restoreQuestionTextDraft(draftContext(), text)).rejects.toThrow('nonempty text')
    expect(browser.events).toEqual([])
  })

  it('rejects a draft that changes after reload', async () => {
    const native = draftContext()
    native.page.reload = async () => {
      browser.draft = 'Another question draft'
      return null
    }
    await expect(restoreQuestionTextDraft(native, 'Typed answer')).rejects.toThrow('the composer retains the question draft')
    expect(browser.events).toContain('stored-draft')
  })
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
    locator: () => ({ fake: 'selected-input', inputValue: async () => 'native-green' }),
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

describe('chooseQuestionOptionWithNote', () => {
  it('restores the selection and then its note before submitting either value', async () => {
    const { banner } = questionBanner()
    browser.restoredBanner = banner
    const native = {
      page: { reload: async () => { browser.events.push('reload') } },
      leapmuxServer: { adminUserId: 'question-user' },
    } as unknown as ManagedNativeScenarioContext
    await chooseQuestionOptionWithNote(native, 'Green', 'Keep this note')(banner)
    expect(browser.events.filter(event => event === 'reload')).toHaveLength(2)
    expect(browser.events.indexOf('stored')).toBeLessThan(browser.events.indexOf('fill-note'))
    expect(browser.events.indexOf('stored-draft')).toBeGreaterThan(browser.events.indexOf('fill-note'))
    expect(browser.events.at(-1)).toBe('click:submit')
    expect(browser.draft).toBe('Keep this note')
  })

  it.each([{ label: '', note: 'Note' }, { label: 'Green', note: '' }, { label: 'Green', note: ' \t' }])('rejects an incomplete note case: %j', ({ label, note }) => {
    expect(() => chooseQuestionOptionWithNote({} as ManagedNativeScenarioContext, label, note)).toThrow('label and nonempty note')
    expect(browser.events).toEqual([])
  })
})

describe('restoreQuestionSelection', () => {
  function selection(value = 'native-green') {
    const input = { fake: 'selected-input', inputValue: async () => value }
    const banner = { getByTestId: () => ({ locator: () => input }) } as unknown as Locator
    browser.restoredBanner = banner
    const native = {
      page: { reload: async () => { browser.events.push('reload') } },
      leapmuxServer: { adminUserId: 'question-user' },
    } as unknown as ManagedNativeScenarioContext
    return { banner, native }
  }

  it('restores the question owner when the narrow layout hides its tab', async () => {
    const { banner, native } = selection()
    browser.mobileOwner = true
    await expect(restoreQuestionSelection(native, banner, 'Green')).resolves.toBeUndefined()
  })

  it('waits for the stored native value before reloading and checks the restored selection', async () => {
    const { banner, native } = selection()
    await restoreQuestionSelection(native, banner, 'Green')
    expect(browser.events).toEqual(['selected-input checked', 'stored', 'reload', 'banner', 'selected-input checked'])
  })

  it('accepts page zero and rejects a matching answer stored on a different page', async () => {
    const { banner, native } = selection()
    browser.stored = { selections: { 0: ['native-green'] }, currentPage: 0 }
    await restoreQuestionSelection(native, banner, 'Green', 0)
    browser.events.length = 0
    browser.stored = { selections: { 0: ['native-green'] }, currentPage: 1 }
    await expect(restoreQuestionSelection(native, banner, 'Green', 1)).rejects.toThrow('the stored selection matches the native value')
    expect(browser.events).not.toContain('reload')
  })

  it.each([-1, 0.5, Number.NaN, Number.POSITIVE_INFINITY, Number.MAX_SAFE_INTEGER + 1])('rejects an invalid page before reading its selection: %s', async (index) => {
    const { banner, native } = selection()
    await expect(restoreQuestionSelection(native, banner, 'Green', index)).rejects.toThrow('nonnegative safe integer')
    expect(browser.events).toEqual([])
  })

  it.each([null, {}, { selections: { 0: ['Green'] } }, { selections: { 0: [] } }])('rejects a missing or incorrect stored selection: %j', async (stored) => {
    const { banner, native } = selection()
    browser.stored = stored
    await expect(restoreQuestionSelection(native, banner, 'Green')).rejects.toThrow('the stored selection matches the native value')
    expect(browser.events).not.toContain('reload')
  })

  it('rejects an empty native option value before reading storage', async () => {
    const { banner, native } = selection('')
    await expect(restoreQuestionSelection(native, banner, 'Green')).rejects.toThrow('no native value')
    expect(browser.events).not.toContain('stored')
  })

  it('rejects a selection that disappears after reload', async () => {
    const { banner, native } = selection()
    native.page.reload = async () => {
      browser.events.push('reload')
      browser.restoredChecked = false
      return null
    }
    await expect(restoreQuestionSelection(native, banner, 'Green')).rejects.toThrow('the restored option stays selected')
    expect(browser.events).toContain('reload')
  })

  it('rejects a different question owner after reload', async () => {
    const { banner, native } = selection()
    native.page.reload = async () => {
      browser.ownerId = 'another-agent'
      return null
    }
    await expect(restoreQuestionSelection(native, banner, 'Green')).rejects.toThrow('the restored question belongs to the original agent')
  })
})

describe('chooseQuestionPages', () => {
  const native = {} as ManagedNativeScenarioContext

  it('requires one answer for each nonempty question list', () => {
    expect(() => chooseQuestionPages(native, [], [])).toThrow('one answer for each question')
    expect(() => chooseQuestionPages(native, [COLOR], [])).toThrow('one answer for each question')
  })

  it.each([{ labels: [] }, { labels: ['Blue', 'Blue'] }, { labels: ['Unknown'] }])('rejects absent, repeated, or unoffered selections: %j', ({ labels }) => {
    expect(() => chooseQuestionPages(native, [COLOR], [{ labels }])).toThrow('distinct offered options')
  })

  it('permits multiple choices only when the native question supports them', () => {
    expect(() => chooseQuestionPages(native, [COLOR], [{ labels: ['Blue', 'Green'] }])).toThrow('one selected option')
    expect(chooseQuestionPages(native, [{ ...COLOR, multiSelect: true }], [{ labels: ['Blue', 'Green'] }])).toBeTypeOf('function')
  })

  it.each(['', ' \t\n'])('rejects an empty typed answer: %j', (text) => {
    expect(() => chooseQuestionPages(native, [COLOR], [{ text }])).toThrow('must contain text')
  })
})
