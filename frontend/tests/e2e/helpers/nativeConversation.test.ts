/**
 * The unit tests check the order of the checks of the basic chat and conversation context scenarios, and that a
 * missing fact fails them. The basic-chat and conversation-context browser specs of each provider check the actual chat.
 */
import type { Page } from '@playwright/test'
import type { MockModelRequestRecord, MockModelStep } from './mockModelScript'
import type { ModelScript } from './modelScriptFixture'
import type { NativeModelTurn, NativeScenarioContext } from './nativeScenario'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { exerciseBasicChat, exerciseConversationContext } from './nativeConversation'

/** The browser actions and checks that the fakes record, in order. */
const recorded = vi.hoisted(() => ({
  events: [] as string[],
  /** Every text that the fake composer sent, in order. */
  sent: [] as string[],
  /** The start of the recorded text of a check that fails, or '' when every check passes. */
  failingPrefix: '',
  /** The text of each fake element, by the description of its locator. A text check of another element always passes. */
  texts: {} as Record<string, string>,
  /** The texts that replace `texts` at the reload, or undefined when the reload keeps them. */
  textsAfterReload: undefined as Record<string, string> | undefined,
}))

/** A fake locator states its selector chain, so each recorded check identifies the element that it reads. */
const fakes = vi.hoisted(() => {
  interface FakeLocator {
    description: string
    filter: (options: { hasText: string | RegExp }) => FakeLocator
    first: () => FakeLocator
    last: () => FakeLocator
  }
  function locator(description: string): FakeLocator {
    return {
      description,
      filter: ({ hasText }) => locator(`${description} with ${String(hasText)}`),
      first: () => locator(`first ${description}`),
      last: () => locator(`last ${description}`),
    }
  }
  return { locator }
})

vi.mock('./ui', () => ({
  assistantBubbles: () => fakes.locator('assistant bubbles'),
  userBubbles: () => fakes.locator('user bubbles'),
  visibleOnly: (locator: { description: string }) => fakes.locator(`visible ${locator.description}`),
  sendMessage: async (_page: Page, text: string) => {
    recorded.sent.push(text)
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
  const subjectOf = (value: unknown) => typeof value === 'string' ? value : (value as { description: string }).description
  return {
    ...actual,
    expect: (value: unknown, message?: string) => ({
      toBeVisible: async () => record(`visible ${subjectOf(value)}`),
      toHaveCount: async (count: number) => record(`count ${count} ${subjectOf(value)}`),
      toHaveText: async (text: RegExp) => {
        const actualText = recorded.texts[subjectOf(value)]
        if (actualText !== undefined && !text.test(actualText))
          throw new Error(`The fake text ${JSON.stringify(actualText)} does not match ${String(text)}.`)
        record(`text ${String(text)} ${subjectOf(value)}`)
      },
      toBeDefined: () => {
        if (value === undefined)
          throw new Error(`The fake value is undefined: ${message}`)
        record(`${message}: defined`)
      },
      toContain: (text: string) => {
        if (typeof value !== 'string' || !value.includes(text))
          throw new Error(`The fake ${message ?? 'request context'} does not contain ${JSON.stringify(text)}.`)
        record(message === undefined ? `contains ${text}` : `${message}: contains ${text}`)
      },
    }),
  }
})

const UNRENDERED = ['LeapMux has no display for this row', 'LeapMux could not render this row']
const INDICATOR = '[data-testid="thinking-indicator"]:visible'
const DIVIDERS = '[data-testid="result-divider"]:visible'
const LAST_DIVIDER = `last ${DIVIDERS}`
const UNTIMED_DIVIDER = String.raw`/^Turn ended(?:\$\d+\.\d{4})?$/`
const TIMED_DIVIDER = String.raw`/^Turn ended \((?:\d+ms|\d+\.\d+s|\d+[dhms](?: \d+[dhms])*)\)(?:\$\d+\.\d{4})?$/`
const USER_TURN_DEFINED = 'the native request holds a user turn that carries the prompt: defined'
const LAST_USER_TURN = 'the last user turn of the native request holds the prompt'
const DATE_REMINDER = '<system-reminder>\nToday\'s date is 2026-10-07.\n</system-reminder>'

/** How a fake context states its model requests and its native answer. */
interface FakeContextOptions {
  /** The step index of the first request, as after earlier turns. */
  start?: number
  /**
   * Whether a request holds every text sent so far and the answers of the steps before it. A context with no history
   * holds only the last text sent, as a provider that starts a new session for each prompt.
   */
  history?: boolean
  /** The user rows that the native agent sends after the last text sent. */
  userRowsAfterPrompt?: readonly string[]
  /** A request with no user row at all. */
  noUserRow?: boolean
  /** The provider's own turn reader, for a request in a shape that the generic reader cannot read. */
  readConversationTurns?: (request: MockModelRequestRecord) => NativeModelTurn[]
  /** The names of the tool calls that deliver the answer. The answer step calls each of them. */
  answerToolCalls?: readonly string[]
  /** The answer tools that the transcript draws as no tool row. */
  answerToolNames?: readonly string[]
}

/** Build the Chat Completions rows of a request: each text sent, each earlier answer after its text, and the extra rows. */
function requestRows(sent: readonly string[], answers: readonly string[], userRowsAfterPrompt: readonly string[]): unknown[] {
  const rows = sent.flatMap((text, index) => {
    const answer = answers[index]
    return answer === undefined ? [{ role: 'user', content: text }] : [{ role: 'user', content: text }, { role: 'assistant', content: answer }]
  })
  return [...rows, ...userRowsAfterPrompt.map(content => ({ role: 'user', content }))]
}

function fakeContext({
  start = 0,
  history = true,
  userRowsAfterPrompt = [],
  noUserRow = false,
  readConversationTurns,
  answerToolCalls = [],
  answerToolNames,
}: FakeContextOptions = {}): NativeScenarioContext {
  const queued: MockModelStep[] = []
  const modelScript = {
    prompt: (text: string) => `${text} [marked]`,
    queue: async (...steps: MockModelStep[]) => {
      const index = start + queued.length
      queued.push(...steps)
      recorded.events.push(`queue ${steps.map(step => step.text).join(', ')}`)
      return index
    },
    waitForSteps: async (count: number) => {
      recorded.events.push(`steps ${count}`)
    },
    requestAt: async (stepIndex: number): Promise<MockModelRequestRecord> => {
      recorded.events.push(`request ${stepIndex}`)
      const answers = queued.slice(0, stepIndex - start).map(step => step.text ?? '')
      const sent = history ? recorded.sent : recorded.sent.slice(-1)
      const messages = noUserRow
        // The context check reads every string of the body, so the prompt stays in the request, outside each user row.
        ? [{ role: 'system', content: sent.join('\n') }]
        : requestRows(sent, history ? answers : [], userRowsAfterPrompt)
      return { protocol: 'openai-chat-completions', path: '/v1/chat/completions', stepIndex, body: { messages } }
    },
  } as unknown as ModelScript
  const answerTools = answerToolCalls.length === 0
    ? {}
    : { textStep: (text: string) => ({ text, toolCalls: answerToolCalls.map((name, index) => ({ id: `answer-${index}`, name, arguments: { text } })) }) }
  const page = {
    locator: (selector: string) => fakes.locator(selector),
    getByText: (text: string) => fakes.locator(`text ${text}`),
    reload: async () => {
      recorded.events.push('reload')
      if (recorded.textsAfterReload !== undefined)
        recorded.texts = recorded.textsAfterReload
      return null
    },
  } as unknown as Page
  return {
    page,
    modelScript,
    provider: AgentProvider.OPENCODE,
    ...answerTools,
    ...(answerToolNames ? { answerToolNames } : {}),
    ...(readConversationTurns ? { readConversationTurns } : {}),
  }
}

beforeEach(() => {
  recorded.events.length = 0
  recorded.sent.length = 0
  recorded.failingPrefix = ''
  recorded.texts = { [LAST_DIVIDER]: 'Turn ended' }
  recorded.textsAfterReload = undefined
})

describe('exerciseBasicChat', () => {
  it('requires the prompt in the last user turn, a turn-ended divider and no unrendered card, live and after reload', async () => {
    const { prompt } = await exerciseBasicChat(fakeContext())
    const answer = prompt.replace(/^Reply once for BASICCHAT(\w+)\.$/, 'BASICANSWER$1')
    expect(answer).not.toBe(prompt)
    const noUnrenderedCard = UNRENDERED.map(title => `count 0 visible text ${title}`)
    expect(recorded.events).toEqual([
      `queue ${answer}`,
      `send ${prompt} [marked]`,
      'steps 1',
      'idle',
      'request 0',
      `contains ${prompt}`,
      `visible first assistant bubbles with ${answer}`,
      USER_TURN_DEFINED,
      `${LAST_USER_TURN}: contains ${prompt}`,
      `visible first user bubbles with ${prompt}`,
      `count 0 ${INDICATOR}`,
      `text ${UNTIMED_DIVIDER} ${LAST_DIVIDER}`,
      ...noUnrenderedCard,
      'reload',
      `visible first user bubbles with ${prompt}`,
      `visible first assistant bubbles with ${answer}`,
      `text ${UNTIMED_DIVIDER} ${LAST_DIVIDER}`,
      ...noUnrenderedCard,
    ])
  })

  it('returns the request of the step that its queue call returned, after an earlier turn', async () => {
    const { request } = await exerciseBasicChat(fakeContext({ start: 5 }))
    expect(request.stepIndex).toBe(5)
    expect(recorded.events).toContain('steps 6')
  })

  it('stops at an unrendered card before the reload', async () => {
    recorded.failingPrefix = 'count 0 visible text LeapMux could not render this row'
    await expect(exerciseBasicChat(fakeContext())).rejects.toThrow('The fake check failed')
    expect(recorded.events).not.toContain('reload')
  })

  it('requires the prompt in the last user turn after earlier turns', async () => {
    recorded.sent.push('An earlier prompt.')
    const { prompt } = await exerciseBasicChat(fakeContext({ start: 1 }))
    expect(recorded.events).toContain(`${LAST_USER_TURN}: contains ${prompt}`)
  })

  it('fails when the native agent sends a user row after the prompt', async () => {
    const context = fakeContext({ userRowsAfterPrompt: [DATE_REMINDER] })
    await expect(exerciseBasicChat(context)).rejects.toThrow(`The fake ${LAST_USER_TURN} does not contain "Reply once for BASICCHAT`)
    // The whole request holds the prompt, so only the last-user-turn check fails.
    expect(recorded.events).toContain(USER_TURN_DEFINED)
    expect(recorded.events.some(event => event.startsWith('text '))).toBe(false)
  })

  it('fails when the native request holds the prompt in no user turn', async () => {
    await expect(exerciseBasicChat(fakeContext({ noUserRow: true }))).rejects.toThrow('The fake value is undefined: the native request holds a user turn')
    expect(recorded.events.some(event => event.startsWith('contains Reply once for BASICCHAT'))).toBe(true)
    expect(recorded.events).not.toContain(USER_TURN_DEFINED)
  })

  it('reads the turns through the provider reader when the context states one', async () => {
    const reader = vi.fn((_request: MockModelRequestRecord): NativeModelTurn[] => [
      { role: 'user', text: 'An earlier prompt.' },
      { role: 'assistant', text: 'An earlier answer.' },
      { role: 'user', text: recorded.sent.at(-1) ?? '' },
    ])
    // The generic reader would read a user row after the prompt and fail.
    const { prompt, request } = await exerciseBasicChat(fakeContext({ userRowsAfterPrompt: [DATE_REMINDER], readConversationTurns: reader }))
    expect(reader).toHaveBeenCalledWith(request)
    expect(recorded.events).toContain(`${LAST_USER_TURN}: contains ${prompt}`)
  })

  it('fails when the provider reader states the prompt in an earlier user turn', async () => {
    const reader = (_request: MockModelRequestRecord): NativeModelTurn[] => [
      { role: 'user', text: recorded.sent.at(-1) ?? '' },
      { role: 'assistant', text: 'An answer.' },
      { role: 'user', text: 'A native reminder.' },
    ]
    await expect(exerciseBasicChat(fakeContext({ readConversationTurns: reader }))).rejects.toThrow(`The fake ${LAST_USER_TURN} does not contain`)
  })

  it.each([
    'Turn ended (377ms)',
    'Turn ended (0ms)',
    'Turn ended (2.3s)',
    'Turn ended (45s)',
    'Turn ended (1m 5s)',
    'Turn ended (1h 2m 3s)',
    'Turn ended (2.3s)$0.0123',
  ])('accepts the timed divider %j', async (text) => {
    recorded.texts = { [LAST_DIVIDER]: text }
    await exerciseBasicChat(fakeContext(), { timedDivider: true })
    expect(recorded.events.filter(event => event === `text ${TIMED_DIVIDER} ${LAST_DIVIDER}`)).toHaveLength(2)
  })

  it.each([
    ['states no duration', 'Turn ended'],
    ['states empty parentheses', 'Turn ended ()'],
    ['states a word for the duration', 'Turn ended (soon)'],
    ['states a qualifier after the duration', 'Turn ended (2.3s, turn limit)'],
    ['states a reason', 'Turn ended (2.3s) — reason'],
    ['states a tool count for a turn with no tool call', 'Turn ended (2.3s)1 tool'],
    ['states another outcome', 'Turn interrupted (2.3s)'],
  ])('rejects a timed divider that %s, before the reload', async (_case, text) => {
    recorded.texts = { [LAST_DIVIDER]: text }
    await expect(exerciseBasicChat(fakeContext(), { timedDivider: true })).rejects.toThrow(`does not match ${TIMED_DIVIDER}`)
    expect(recorded.events).not.toContain('reload')
  })

  it.each([
    ['states a duration', 'Turn ended (2.3s)'],
    ['states a qualifier', 'Turn ended (end_turn)'],
    ['states a tool count for a turn with no tool call', 'Turn ended1 tool'],
  ])('rejects an untimed divider that %s', async (_case, text) => {
    recorded.texts = { [LAST_DIVIDER]: text }
    await expect(exerciseBasicChat(fakeContext())).rejects.toThrow(`does not match ${UNTIMED_DIVIDER}`)
  })

  it.each([
    ['one drawn answer tool', ['respond'], [], 'Turn ended1 tool'],
    ['one drawn answer tool in a priced turn', ['respond'], [], 'Turn ended1 tool \u00B7 $0.0012'],
    ['two drawn answer tools', ['respond', 'report'], [], 'Turn ended2 tools'],
    ['an answer tool that the transcript does not draw', ['answer'], ['answer'], 'Turn ended'],
    ['a drawn tool beside an answer tool that the transcript does not draw', ['respond', 'answer'], ['answer'], 'Turn ended1 tool'],
  ])('requires the tool count of %s', async (_case, answerToolCalls, answerToolNames, text) => {
    recorded.texts = { [LAST_DIVIDER]: text }
    await exerciseBasicChat(fakeContext({ answerToolCalls, answerToolNames }))
    expect(recorded.events.filter(event => event.startsWith('text ') && event.endsWith(LAST_DIVIDER))).toHaveLength(2)
  })

  it.each([
    ['no tool count for a drawn answer tool', ['respond'], [], 'Turn ended'],
    ['a tool count for an answer tool that the transcript does not draw', ['answer'], ['answer'], 'Turn ended1 tool'],
    ['another tool count', ['respond'], [], 'Turn ended2 tools'],
  ])('rejects %s', async (_case, answerToolCalls, answerToolNames, text) => {
    recorded.texts = { [LAST_DIVIDER]: text }
    await expect(exerciseBasicChat(fakeContext({ answerToolCalls, answerToolNames }))).rejects.toThrow(`The fake text ${JSON.stringify(text)} does not match`)
  })

  it('accepts the cost of a priced turn after an untimed divider', async () => {
    recorded.texts = { [LAST_DIVIDER]: 'Turn ended$0.0012' }
    await exerciseBasicChat(fakeContext())
    expect(recorded.events.filter(event => event === `text ${UNTIMED_DIVIDER} ${LAST_DIVIDER}`)).toHaveLength(2)
  })

  it('fails when the saved turn end loses its duration after the reload', async () => {
    recorded.texts = { [LAST_DIVIDER]: 'Turn ended (2.3s)' }
    recorded.textsAfterReload = { [LAST_DIVIDER]: 'Turn ended' }
    await expect(exerciseBasicChat(fakeContext(), { timedDivider: true })).rejects.toThrow('The fake text "Turn ended" does not match')
    expect(recorded.events).toContain('reload')
  })
})

describe('exerciseConversationContext', () => {
  it('requires the first exchange in the second request, and the first answer in the chat after the second turn', async () => {
    await exerciseConversationContext(fakeContext({ start: 2 }))
    const [firstPrompt, secondPrompt] = recorded.sent.map(text => text.replace(/ \[marked\]$/, ''))
    const firstAnswer = firstPrompt!.replace(/^Keep CONTEXTPROMPT(\w+) for the conversation\.$/, 'CONTEXTANSWER$1')
    expect(firstAnswer).not.toBe(firstPrompt)
    const afterSecondTurn = recorded.events.slice(recorded.events.indexOf('request 3'))
    expect(afterSecondTurn).toEqual([
      'request 3',
      `contains ${secondPrompt}`,
      expect.stringMatching(/^visible first assistant bubbles with NEXTANSWER\w+$/),
      `contains ${firstPrompt}`,
      `contains ${firstAnswer}`,
      `visible first assistant bubbles with ${firstAnswer}`,
      'count 2 user bubbles',
      `count 2 ${DIVIDERS}`,
    ])
  })

  it('fails when the second request lacks the first exchange', async () => {
    await expect(exerciseConversationContext(fakeContext({ history: false }))).rejects.toThrow('does not contain "Keep CONTEXTPROMPT')
    expect(recorded.events).not.toContain('count 2 user bubbles')
  })
})
