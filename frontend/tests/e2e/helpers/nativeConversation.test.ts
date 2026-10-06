/**
 * The unit tests check the order of the checks of the basic chat and conversation context scenarios, and that a
 * missing fact fails them. The basic-chat and conversation-context browser specs of each provider check the actual chat.
 */
import type { Page } from '@playwright/test'
import type { MockModelRequestRecord, MockModelStep } from './mockModelScript'
import type { ModelScript } from './modelScriptFixture'
import type { NativeScenarioContext } from './nativeScenario'
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
  return {
    ...actual,
    expect: (value: string | { description: string }) => ({
      toBeVisible: async () => record(`visible ${typeof value === 'string' ? value : value.description}`),
      toHaveCount: async (count: number) => record(`count ${count} ${typeof value === 'string' ? value : value.description}`),
      toHaveText: async (text: string | RegExp) => record(`text ${String(text)} ${typeof value === 'string' ? value : value.description}`),
      toContain: (text: string) => {
        if (typeof value !== 'string' || !value.includes(text))
          throw new Error(`The fake request context does not contain ${JSON.stringify(text)}.`)
        record(`contains ${text}`)
      },
    }),
  }
})

const UNRENDERED = ['LeapMux has no display for this row', 'LeapMux could not render this row']
const INDICATOR = '[data-testid="thinking-indicator"]:visible'
const DIVIDERS = '[data-testid="result-divider"]:visible'

/**
 * A context whose script starts at `start`, as after earlier turns.
 * The request of a step holds every text sent so far and the answers of the steps before it. A context with no
 * history holds only the last text sent, as a provider that starts a new session for each prompt.
 */
function fakeContext(start: number, history = true): NativeScenarioContext {
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
      const answers = queued.slice(0, stepIndex - start).map(step => step.text)
      const body = history ? { sent: [...recorded.sent], answers } : { sent: recorded.sent.slice(-1) }
      return { protocol: 'openai-chat-completions', path: '/v1/chat/completions', stepIndex, body }
    },
  } as unknown as ModelScript
  const page = {
    locator: (selector: string) => fakes.locator(selector),
    getByText: (text: string) => fakes.locator(`text ${text}`),
    reload: async () => {
      recorded.events.push('reload')
      return null
    },
  } as unknown as Page
  return { page, modelScript, provider: AgentProvider.OPENCODE }
}

beforeEach(() => {
  recorded.events.length = 0
  recorded.sent.length = 0
  recorded.failingPrefix = ''
})

describe('exerciseBasicChat', () => {
  it('requires a turn-ended divider and no unrendered card, live and after reload', async () => {
    const { prompt } = await exerciseBasicChat(fakeContext(0))
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
      `visible first user bubbles with ${prompt}`,
      `count 0 ${INDICATOR}`,
      `text /^Turn ended/ last ${DIVIDERS}`,
      ...noUnrenderedCard,
      'reload',
      `visible first user bubbles with ${prompt}`,
      `visible first assistant bubbles with ${answer}`,
      ...noUnrenderedCard,
    ])
  })

  it('returns the request of the step that its queue call returned, after an earlier turn', async () => {
    const { request } = await exerciseBasicChat(fakeContext(5))
    expect(request.stepIndex).toBe(5)
    expect(recorded.events).toContain('steps 6')
  })

  it('stops at an unrendered card before the reload', async () => {
    recorded.failingPrefix = 'count 0 visible text LeapMux could not render this row'
    await expect(exerciseBasicChat(fakeContext(0))).rejects.toThrow('The fake check failed')
    expect(recorded.events).not.toContain('reload')
  })
})

describe('exerciseConversationContext', () => {
  it('requires the first exchange in the second request, and the first answer in the chat after the second turn', async () => {
    await exerciseConversationContext(fakeContext(2))
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
    await expect(exerciseConversationContext(fakeContext(0, false))).rejects.toThrow('does not contain "Keep CONTEXTPROMPT')
    expect(recorded.events).not.toContain('count 2 user bubbles')
  })
})
