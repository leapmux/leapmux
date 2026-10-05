import type { Page } from '@playwright/test'
import type { AgentChatMessage, AgentInfo } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import type { NativeMessageSnapshot } from './nativeMessages'
import type { NativeModelTurn } from './nativeScenario'
import { create } from '@bufbuild/protobuf'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { AgentChatMessageSchema, AgentInfoSchema, AgentProvider, AgentStatus, ContentCompression } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import {
  countOriginalAnswerRows,
  expectNativeResumeContext,
  expectReopenedNativeAgent,
  expectResumedAnswerUnmerged,
  expectResumedConversation,
  nativeResumeContextOrder,
  nativeResumeTexts,
  reopenedNativeAgentVerdict,
} from './nativeResume'

/** The poll fixture reads a value at most this many times, as a bounded stand-in for the Playwright timeout. */
const POLL_ATTEMPTS = 5

const calls = vi.hoisted(() => ({
  agent: vi.fn<(context: unknown, agentId: string) => Promise<AgentInfo | null>>(),
  snapshot: vi.fn<(context: unknown, agentId: string) => Promise<NativeMessageSnapshot>>(),
  user: [] as string[],
  assistant: [] as string[],
}))

vi.mock('./nativeScenario', async importOriginal => ({
  ...await importOriginal<typeof import('./nativeScenario')>(),
  nativeAgentById: calls.agent,
}))
vi.mock('./nativeMessages', async importOriginal => ({
  ...await importOriginal<typeof import('./nativeMessages')>(),
  readNativeMessageSnapshot: calls.snapshot,
}))

/** Locate visible bubbles in memory. Each filter keeps the bubbles that hold its text. */
function bubbles(texts: () => readonly string[], filters: readonly string[] = []) {
  const matching = () => texts().filter(text => filters.every(filter => text.includes(filter)))
  return {
    bubbleProbe: true,
    count: () => matching().length,
    filter: ({ hasText }: { hasText: string }) => bubbles(texts, [...filters, hasText]),
    first: () => ({ bubbleProbe: true, count: () => Math.min(1, matching().length) }),
  }
}

vi.mock('./ui', async importOriginal => ({
  ...await importOriginal<typeof import('./ui')>(),
  assistantBubbles: () => bubbles(() => calls.assistant),
  userBubbles: () => bubbles(() => calls.user),
}))

vi.mock('@playwright/test', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@playwright/test')>()
  const check = (value: unknown, message?: string) => {
    if (typeof value === 'object' && value !== null && 'bubbleProbe' in value && 'count' in value && typeof value.count === 'function') {
      const count = value.count
      return {
        toBeVisible: async () => expect(count(), message).toBeGreaterThan(0),
        toHaveCount: async (expected: number) => expect(count(), message).toBe(expected),
      }
    }
    return expect(value, message)
  }
  const poll = (read: () => Promise<unknown>, options?: { message?: string }) => {
    const settle = async (accept: (value: unknown) => boolean) => {
      let last: unknown
      for (let attempt = 0; attempt < POLL_ATTEMPTS; attempt++) {
        last = await read()
        if (accept(last))
          return
      }
      throw new Error(`${options?.message ?? 'The poll did not pass.'} Last value: ${JSON.stringify(last)}`)
    }
    return {
      toBe: (expected: unknown) => settle(value => Object.is(value, expected)),
      not: { toBe: (expected: unknown) => settle(value => !Object.is(value, expected)) },
    }
  }
  return { ...actual, expect: Object.assign(check, { poll }) }
})

const MARKER = '0123456789abcdef0123456789abcdef'
const texts = nativeResumeTexts(MARKER)
const server = { hubUrl: 'http://unit.invalid', adminToken: 'unit-token', workerId: 'unit-worker' }
const encoder = new TextEncoder()

function row(id: string, seq: bigint, body: unknown, supplement?: unknown): AgentChatMessage {
  return create(AgentChatMessageSchema, {
    id,
    seq,
    agentSessionId: 'native-session',
    content: encoder.encode(JSON.stringify(body)),
    contentCompression: ContentCompression.NONE,
    ...(supplement === undefined ? {} : { supplementalContent: encoder.encode(JSON.stringify(supplement)), supplementalContentCompression: ContentCompression.NONE }),
  })
}

function snapshot(agentId: string, messages: AgentChatMessage[]): NativeMessageSnapshot {
  return { agentId, agentSessionId: 'native-session', messages }
}

function agent(fields: Partial<Pick<AgentInfo, 'id' | 'status' | 'agentProvider' | 'agentSessionId' | 'startupError'>>): AgentInfo {
  return create(AgentInfoSchema, { id: 'reopened', status: AgentStatus.ACTIVE, agentProvider: AgentProvider.GEMINI_CLI, agentSessionId: 'stored-session', ...fields })
}

const stored = { agentProvider: AgentProvider.GEMINI_CLI, agentSessionId: 'stored-session' }

/** Return each queued value once, then repeat the last value. */
function sequence<T>(values: readonly T[]): () => T {
  let index = 0
  return () => {
    const value = values[Math.min(index, values.length - 1)]
    index++
    if (value === undefined)
      throw new Error('The sequence fixture requires at least one value.')
    return value
  }
}

/** Build a page whose selected agent tab reports the queued tab IDs. */
function selectedTabPage(ids: readonly (readonly string[])[]) {
  const next = sequence(ids)
  const selectors: string[] = []
  const page = Object.assign({} as Page, {
    locator: (selector: string) => {
      selectors.push(selector)
      return {
        first: () => ({
          evaluateAll: async (read: (tabs: { getAttribute: (name: string) => string | null }[]) => string[]) =>
            read(next().map(id => ({ getAttribute: (name: string) => name === 'data-tab-id' ? id : null }))),
        }),
      }
    },
  })
  return { page, selectors }
}

beforeEach(() => {
  vi.resetAllMocks()
  calls.user.length = 0
  calls.assistant.length = 0
})

describe('nativeResumeTexts', () => {
  it('builds four texts that each hold the one marker', () => {
    expect(texts).toEqual({
      marker: MARKER,
      originalPrompt: `Keep RESUMEPROMPT${MARKER} for the stored session.`,
      originalAnswer: `RESUMEANSWER${MARKER}`,
      resumedPrompt: `Reply to RESUMEDPROMPT${MARKER} in the reopened session.`,
      resumedAnswer: `RESUMEDNEWANSWER${MARKER}`,
    })
  })

  it.each([MARKER, undefined])('builds texts where no text contains another for the marker %s', (marker) => {
    const built = nativeResumeTexts(marker)
    const values = [built.originalPrompt, built.originalAnswer, built.resumedPrompt, built.resumedAnswer]
    for (const [index, value] of values.entries()) {
      expect(value).toContain(built.marker)
      for (const [other, otherValue] of values.entries()) {
        if (other !== index)
          expect(value.includes(otherValue)).toBe(false)
      }
    }
  })

  it('generates a new 32-digit lowercase hexadecimal marker for each scenario', () => {
    const first = nativeResumeTexts().marker
    const second = nativeResumeTexts().marker
    expect(first).toMatch(/^[a-f0-9]{32}$/)
    expect(second).toMatch(/^[a-f0-9]{32}$/)
    expect(first).not.toBe(second)
  })

  it.each(['', MARKER.slice(1), `${MARKER}0`, MARKER.toUpperCase(), `${MARKER.slice(1)}g`, '01234567-89ab-cdef-0123-456789abcdef', ` ${MARKER.slice(1)}`])('rejects the invalid marker "%s"', (marker) => {
    expect(() => nativeResumeTexts(marker)).toThrow('32 lowercase hexadecimal digits')
  })
})

describe('nativeResumeContextOrder', () => {
  it('finds the first user prompt, assistant answer, and resumed prompt', () => {
    const turns: NativeModelTurn[] = [
      { role: 'user', text: 'System context.' },
      { role: 'user', text: `${texts.originalPrompt}\n\nscenario marker` },
      { role: 'assistant', text: texts.originalAnswer },
      { role: 'user', text: texts.resumedPrompt },
      { role: 'assistant', text: texts.originalAnswer },
    ]
    expect(nativeResumeContextOrder(turns, texts)).toEqual({ originalPrompt: 1, originalAnswer: 2, resumedPrompt: 3 })
  })

  it('requires the role of each text', () => {
    const turns: NativeModelTurn[] = [
      { role: 'assistant', text: texts.originalPrompt },
      { role: 'user', text: texts.originalAnswer },
      { role: 'assistant', text: texts.resumedPrompt },
    ]
    expect(nativeResumeContextOrder(turns, texts)).toEqual({ originalPrompt: -1, originalAnswer: -1, resumedPrompt: -1 })
  })

  it('returns -1 for each text of an empty request', () => {
    expect(nativeResumeContextOrder([], texts)).toEqual({ originalPrompt: -1, originalAnswer: -1, resumedPrompt: -1 })
  })

  it('does not accept the bare marker family as the exact text', () => {
    const turns: NativeModelTurn[] = [
      { role: 'user', text: 'Keep RESUMEPROMPT for the stored session.' },
      { role: 'assistant', text: 'RESUMEANSWER' },
      { role: 'user', text: texts.resumedPrompt },
    ]
    expect(nativeResumeContextOrder(turns, texts)).toEqual({ originalPrompt: -1, originalAnswer: -1, resumedPrompt: 2 })
  })

  it.each(['originalPrompt', 'originalAnswer', 'resumedPrompt'] as const)('rejects an empty %s', (field) => {
    expect(() => nativeResumeContextOrder([], { ...texts, [field]: ' ' })).toThrow('nonempty text')
  })
})

describe('expectNativeResumeContext', () => {
  const prompt: NativeModelTurn = { role: 'user', text: texts.originalPrompt }
  const answer: NativeModelTurn = { role: 'assistant', text: texts.originalAnswer }
  const resumed: NativeModelTurn = { role: 'user', text: texts.resumedPrompt }
  const ordered = [prompt, answer, resumed]

  it('accepts the original prompt, the original answer, and the resumed prompt in order', () => {
    expect(() => expectNativeResumeContext(ordered, texts)).not.toThrow()
  })

  it('refuses an absent original prompt', () => {
    expect(() => expectNativeResumeContext([answer, resumed], texts)).toThrow('original prompt in a user turn')
  })

  it('refuses an original answer before the original prompt', () => {
    expect(() => expectNativeResumeContext([answer, prompt, resumed], texts)).toThrow('original answer in an assistant turn after the original prompt')
  })

  it('refuses an original answer that only a user turn holds', () => {
    const summary: NativeModelTurn = { role: 'user', text: `Earlier answer: ${texts.originalAnswer}` }
    expect(() => expectNativeResumeContext([prompt, summary, resumed], texts)).toThrow('original answer in an assistant turn')
  })

  it('refuses a resumed prompt before the original answer', () => {
    expect(() => expectNativeResumeContext([prompt, resumed, answer], texts)).toThrow('resumed prompt in a user turn after the original answer')
  })
})

describe('reopenedNativeAgentVerdict', () => {
  it('returns the active agent in the stored provider and session', () => {
    const reopened = agent({})
    expect(reopenedNativeAgentVerdict(reopened, stored)).toBe(reopened)
  })

  it('throws the startup error of the Worker', () => {
    expect(() => reopenedNativeAgentVerdict(agent({ status: AgentStatus.STARTUP_FAILED, startupError: ' -32603 No previous sessions found ' }), stored))
      .toThrow('The Worker failed to start the resumed native session: -32603 No previous sessions found')
  })

  it.each(['', '  '])('states the absent startup error "%s"', (startupError) => {
    expect(() => reopenedNativeAgentVerdict(agent({ status: AgentStatus.STARTUP_FAILED, startupError }), stored)).toThrow('(the Worker reported no startup error)')
  })

  it.each([
    [AgentStatus.INACTIVE, 'INACTIVE'],
    [AgentStatus.STARTING, 'STARTING'],
    [AgentStatus.UNSPECIFIED, 'UNSPECIFIED'],
  ])('refuses the Worker status %s', (status, name) => {
    expect(() => reopenedNativeAgentVerdict(agent({ status }), stored)).toThrow(`status ${name}, not ACTIVE`)
  })

  it('refuses another provider', () => {
    expect(() => reopenedNativeAgentVerdict(agent({ agentProvider: AgentProvider.CODEX }), stored)).toThrow('not the stored provider')
  })

  it.each(['another-session', '', 'stored-session '])('refuses the confirmed session "%s"', (agentSessionId) => {
    expect(() => reopenedNativeAgentVerdict(agent({ agentSessionId }), stored)).toThrow('not the stored session "stored-session"')
  })

  it.each(['', '  '])('requires the stored session ID "%s"', (agentSessionId) => {
    expect(() => reopenedNativeAgentVerdict(agent({ agentSessionId }), { ...stored, agentSessionId })).toThrow('requires the stored native session ID')
  })
})

describe('expectReopenedNativeAgent', () => {
  it('waits past an earlier tab and STARTING, then returns the active stored session', async () => {
    const { page, selectors } = selectedTabPage([['keeper'], [], ['original'], ['reopened']])
    const reopened = agent({})
    calls.agent.mockResolvedValueOnce(null)
      .mockResolvedValueOnce(agent({ status: AgentStatus.STARTING, agentSessionId: '' }))
      .mockResolvedValueOnce(reopened)
    const context = { page, leapmuxServer: server }
    expect(await expectReopenedNativeAgent(context, stored, ['keeper', 'original'])).toBe(reopened)
    expect(selectors).toEqual(['[data-testid="tab"][data-tab-type="agent"][aria-selected="true"]:visible'])
    expect(calls.agent.mock.calls).toEqual([[context, 'reopened'], [context, 'reopened'], [context, 'reopened']])
  })

  it('throws the Worker startup error once the startup ends', async () => {
    const { page } = selectedTabPage([['reopened']])
    calls.agent.mockResolvedValueOnce(agent({ status: AgentStatus.STARTING, agentSessionId: '' }))
      .mockResolvedValueOnce(agent({ status: AgentStatus.STARTUP_FAILED, agentSessionId: '', startupError: 'session/load: -32603 No previous sessions found' }))
      .mockResolvedValue(agent({}))
    await expect(expectReopenedNativeAgent({ page, leapmuxServer: server }, stored, ['keeper']))
      .rejects
      .toThrow('The Worker failed to start the resumed native session: session/load: -32603 No previous sessions found')
    expect(calls.agent).toHaveBeenCalledTimes(2)
  })

  it('keeps the first verdict after STARTING', async () => {
    const { page } = selectedTabPage([['reopened']])
    calls.agent.mockResolvedValueOnce(agent({ status: AgentStatus.INACTIVE }))
      .mockResolvedValue(agent({}))
    await expect(expectReopenedNativeAgent({ page, leapmuxServer: server }, stored, [])).rejects.toThrow('status INACTIVE, not ACTIVE')
    expect(calls.agent).toHaveBeenCalledTimes(1)
  })

  it('refuses an active agent in another native session', async () => {
    const { page } = selectedTabPage([['reopened']])
    calls.agent.mockResolvedValue(agent({ agentSessionId: 'new-native-session' }))
    await expect(expectReopenedNativeAgent({ page, leapmuxServer: server }, stored, [])).rejects.toThrow('confirmed the session "new-native-session"')
  })

  it('reads no Worker state while the picker selects only an earlier tab', async () => {
    const { page } = selectedTabPage([['keeper']])
    await expect(expectReopenedNativeAgent({ page, leapmuxServer: server }, stored, ['keeper'])).rejects.toThrow('The picker must select the agent tab that it opened.')
    expect(calls.agent).not.toHaveBeenCalled()
  })

  it('fails when the Worker never ends the startup', async () => {
    const { page } = selectedTabPage([['reopened']])
    calls.agent.mockResolvedValue(agent({ status: AgentStatus.STARTING, agentSessionId: '' }))
    await expect(expectReopenedNativeAgent({ page, leapmuxServer: server }, stored, [])).rejects.toThrow('The Worker must end the startup of the reopened agent.')
    expect(calls.agent).toHaveBeenCalledTimes(POLL_ATTEMPTS)
  })
})

describe('countOriginalAnswerRows', () => {
  it('counts each row that holds the original answer in its content or supplement', async () => {
    calls.snapshot.mockResolvedValue(snapshot('original', [
      row('prompt', 1n, { role: 'user', text: texts.originalPrompt }),
      row('answer', 2n, { message: { content: [{ type: 'text', text: texts.originalAnswer }] } }),
      row('result', 3n, { type: 'result' }, { plain: `Final: ${texts.originalAnswer}` }),
      row('turn-end', 4n, { type: 'turn_end', tokens: 0 }),
    ]))
    const context = { leapmuxServer: server }
    expect(await countOriginalAnswerRows(context, 'original', texts)).toBe(2)
    expect(calls.snapshot).toHaveBeenCalledExactlyOnceWith(context, 'original')
  })

  it('refuses an original agent that stored no answer row', async () => {
    calls.snapshot.mockResolvedValue(snapshot('original', [row('prompt', 1n, { text: texts.originalPrompt })]))
    await expect(countOriginalAnswerRows({ leapmuxServer: server }, 'original', texts)).rejects.toThrow('stored no Worker row that holds the original answer')
  })

  it('propagates the Worker read failure', async () => {
    const failure = new Error('The native message read requires a started agent.')
    calls.snapshot.mockRejectedValue(failure)
    await expect(countOriginalAnswerRows({ leapmuxServer: server }, 'original', texts)).rejects.toBe(failure)
  })
})

describe('expectResumedConversation', () => {
  const page = {} as Page
  const context = { page, leapmuxServer: server }
  const originalRow = row('reopened:2', 2n, { text: texts.originalAnswer })
  const resumedRow = row('resumed-answer', 7n, { text: texts.resumedAnswer })

  function settledPage() {
    calls.user.push(texts.originalPrompt, texts.resumedPrompt)
    calls.assistant.push(texts.originalAnswer, texts.resumedAnswer)
  }

  it('accepts one copy of each original row and a separate resumed answer', async () => {
    settledPage()
    calls.snapshot.mockResolvedValue(snapshot('reopened', [originalRow, row('resumed-prompt', 6n, { text: texts.resumedPrompt }), resumedRow]))
    await expect(expectResumedConversation(context, 'reopened', texts, 1)).resolves.toBeUndefined()
    expect(calls.snapshot).toHaveBeenCalledExactlyOnceWith(context, 'reopened')
  })

  it('accepts an original answer that the original agent stored in two rows and drew twice', async () => {
    settledPage()
    calls.assistant.push(texts.originalAnswer)
    calls.snapshot.mockResolvedValue(snapshot('reopened', [originalRow, row('reopened:3', 3n, { tool: { input: texts.originalAnswer } }), resumedRow]))
    await expect(expectResumedConversation(context, 'reopened', texts, 2, 2)).resolves.toBeUndefined()
  })

  it('accepts an original answer that two stored rows drew as one bubble', async () => {
    settledPage()
    calls.snapshot.mockResolvedValue(snapshot('reopened', [originalRow, row('reopened:3', 3n, { tool: { input: texts.originalAnswer } }), resumedRow]))
    await expect(expectResumedConversation(context, 'reopened', texts, 2)).resolves.toBeUndefined()
  })

  it('refuses a resumed answer bubble that holds the replayed original answer', async () => {
    calls.user.push(texts.originalPrompt, texts.resumedPrompt)
    calls.assistant.push(texts.originalAnswer, `${texts.originalAnswer}${texts.resumedAnswer}`)
    calls.snapshot.mockResolvedValue(snapshot('reopened', [originalRow, resumedRow]))
    await expect(expectResumedConversation(context, 'reopened', texts, 1)).rejects.toThrow('The resumed answer bubble must not hold the original answer.')
    expect(calls.snapshot).not.toHaveBeenCalled()
  })

  it('refuses a resumed answer row that holds the replayed original answer', async () => {
    settledPage()
    calls.snapshot.mockResolvedValue(snapshot('reopened', [originalRow, row('merged', 7n, { text: `${texts.originalAnswer}${texts.resumedAnswer}` })]))
    await expect(expectResumedConversation(context, 'reopened', texts, 1)).rejects.toThrow('A Worker row of the resumed answer must not hold the original answer.')
  })

  it('refuses a replayed original answer that the resumed agent stored as a new row', async () => {
    settledPage()
    calls.snapshot.mockResolvedValue(snapshot('reopened', [originalRow, row('replayed', 5n, { text: texts.originalAnswer }), resumedRow]))
    await expect(expectResumedConversation(context, 'reopened', texts, 1)).rejects.toThrow('must store no new Worker row that holds the original answer')
  })

  it('refuses a second original answer bubble', async () => {
    settledPage()
    calls.assistant.push(texts.originalAnswer)
    calls.snapshot.mockResolvedValue(snapshot('reopened', [originalRow, resumedRow]))
    await expect(expectResumedConversation(context, 'reopened', texts, 1)).rejects.toMatchObject({ actual: 2, expected: 1 })
  })

  it('refuses a second original prompt bubble', async () => {
    settledPage()
    calls.user.push(texts.originalPrompt)
    calls.snapshot.mockResolvedValue(snapshot('reopened', [originalRow, resumedRow]))
    await expect(expectResumedConversation(context, 'reopened', texts, 1)).rejects.toMatchObject({ actual: 2, expected: 1 })
  })

  it('refuses an absent resumed answer bubble', async () => {
    calls.user.push(texts.originalPrompt)
    calls.assistant.push(texts.originalAnswer)
    calls.snapshot.mockResolvedValue(snapshot('reopened', [originalRow, resumedRow]))
    await expect(expectResumedConversation(context, 'reopened', texts, 1)).rejects.toMatchObject({ actual: 0 })
  })

  it('refuses an absent resumed answer row', async () => {
    settledPage()
    calls.snapshot.mockResolvedValue(snapshot('reopened', [originalRow]))
    await expect(expectResumedConversation(context, 'reopened', texts, 1)).rejects.toThrow('must store a Worker row that holds the resumed answer')
  })

  it.each([0, -1, 1.5, Number.NaN, Number.POSITIVE_INFINITY])('rejects the original row count %s before any read', async (count) => {
    settledPage()
    await expect(expectResumedConversation(context, 'reopened', texts, count)).rejects.toThrow('positive count of original answer rows')
    expect(calls.snapshot).not.toHaveBeenCalled()
  })
})

describe('expectResumedAnswerUnmerged', () => {
  const page = {} as Page
  const context = { page, leapmuxServer: server }

  it('accepts an external session that shows and stores no original answer', async () => {
    calls.assistant.push(texts.resumedAnswer)
    calls.snapshot.mockResolvedValue(snapshot('reopened', [row('resumed-answer', 1n, { text: texts.resumedAnswer })]))
    await expect(expectResumedAnswerUnmerged(context, 'reopened', texts)).resolves.toBeUndefined()
  })

  it('refuses a merged resumed answer in an external session', async () => {
    calls.assistant.push(`${texts.originalAnswer} ${texts.resumedAnswer}`)
    await expect(expectResumedAnswerUnmerged(context, 'reopened', texts)).rejects.toThrow('The resumed answer bubble must not hold the original answer.')
  })
})
