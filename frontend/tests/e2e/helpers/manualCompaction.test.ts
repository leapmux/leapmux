import type { Page } from '@playwright/test'
import type { NativeCompactionOptions } from './manualCompaction'
import type { MockModelRequestRecord, MockModelRule, MockModelStep } from './mockModelScript'
import type { ModelScript } from './modelScriptFixture'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { CLAUDE_SUMMARIZER_PATTERN, compactionSummaryText, exerciseNativeCompaction, MANUAL_COMPACTION_MARKER, MANUAL_COMPACTION_SUMMARY, OLDER_CONTEXT_MARKER } from './manualCompaction'
import { matchesRequest } from './mockModelScript'

/** A simulated native agent: what it sent, what the transcript shows, and how it compacts. */
const agent = vi.hoisted(() => ({
  send: (_text: string) => {},
  transcript: [] as string[],
}))

vi.mock('./ui', () => ({
  sendMessage: async (_page: Page, text: string) => agent.send(text),
  waitForAgentIdle: async () => {},
  assistantBubbles: () => ({ filter: ({ hasText }: { hasText: string }) => ({ first: () => ({ fakeText: hasText }) }) }),
  messageBubbles: () => ({ filter: ({ hasText }: { hasText: string }) => ({ first: () => ({ fakeText: hasText }) }) }),
}))

vi.mock('@playwright/test', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@playwright/test')>()
  const fake = Object.assign((value: unknown, message?: string) => {
    if (typeof value === 'object' && value !== null && 'fakeText' in value && typeof value.fakeText === 'string') {
      const text = value.fakeText
      return { toBeVisible: async () => expect(agent.transcript.join('\n'), `the transcript shows ${text}`).toContain(text) }
    }
    return expect(value, message)
  }, { poll: expect.poll })
  return { ...actual, expect: fake }
})

interface SimulatedAgentOptions {
  /** Steps that an earlier turn of the test consumed. */
  earlierSteps?: number
  /** The text of the native summarizer request. */
  summarizerText?: string
  /** The agent adds the summary to its history, but keeps the whole history before it. */
  keepsHistory?: boolean
  /** The message that the agent writes when its compaction ends. */
  completion?: string
}

/** A model script and a native agent that answers each prompt from it and compacts on `/compact`. */
function simulatedAgent(options: SimulatedAgentOptions = {}): { script: ModelScript, requests: MockModelRequestRecord[] } {
  const steps: MockModelStep[] = Array.from({ length: options.earlierSteps ?? 0 }, () => ({ text: 'An earlier turn.' }))
  const requests: MockModelRequestRecord[] = steps.map((_, stepIndex) => ({ stepIndex, body: { earlier: true } }) as unknown as MockModelRequestRecord)
  let nextStep = steps.length
  const rules: MockModelRule[] = []
  const ruleMatches: Record<string, number> = {}
  let fallback: MockModelStep | undefined
  let history: string[] = []
  const request = (texts: string[], extra: Partial<MockModelRequestRecord> = {}) => ({ body: { messages: texts }, ...extra }) as unknown as MockModelRequestRecord
  const answer = (step: MockModelStep | undefined) => {
    if (!step)
      throw new Error('The simulated agent found no scripted answer.')
    return step.text ?? ''
  }
  agent.transcript = []
  agent.send = (text) => {
    if (text !== '/compact') {
      history.push(text)
      const record = { ...request([...history]), stepIndex: nextStep }
      requests.push(record)
      const reply = answer(steps[nextStep++])
      history.push(reply)
      agent.transcript.push(reply)
      return
    }
    const summarizer = options.summarizerText ?? 'Summarize the conversation.'
    const texts = [...history, summarizer]
    const rule = rules.find(candidate => typeof candidate.when.user === 'string' && summarizer.includes(candidate.when.user))
    let summary: string
    if (rule) {
      ruleMatches[rule.name] = (ruleMatches[rule.name] ?? 0) + 1
      requests.push(request(texts))
      summary = answer(rule.respond)
    }
    else if (nextStep < steps.length) {
      requests.push({ ...request(texts), stepIndex: nextStep })
      summary = answer(steps[nextStep++])
    }
    else {
      requests.push(request(texts, { fallback: true }))
      summary = answer(fallback)
    }
    history = options.keepsHistory ? [...history, summary] : [summary]
    if (options.completion)
      agent.transcript.push(options.completion)
  }
  const script = {
    prompt: (text: string) => `${text}\n\nMARKER`,
    queue: async (...queued: MockModelStep[]) => {
      const index = steps.length
      steps.push(...queued)
      return index
    },
    rule: async (...added: MockModelRule[]) => {
      rules.push(...added)
    },
    fallback: async (step: MockModelStep) => {
      fallback = step
    },
    waitForSteps: async (count: number) => {
      if (nextStep < count)
        throw new Error(`The simulated agent consumed ${nextStep} steps, not ${count}.`)
    },
    requestAt: async (stepIndex: number) => {
      const record = requests.find(candidate => candidate.stepIndex === stepIndex)
      if (!record)
        throw new Error(`The simulated agent sent no request for step ${stepIndex}.`)
      return record
    },
    status: async () => ({ requests, ruleMatches }),
  } as unknown as ModelScript
  return { script, requests }
}

function context(script: ModelScript) {
  return { page: {} as Page, modelScript: script, provider: AgentProvider.ZCODE }
}

beforeEach(() => {
  agent.transcript = []
})

describe('CLAUDE_SUMMARIZER_PATTERN', () => {
  const request = (userText: string) => ({ protocol: 'anthropic-messages' as const, systemText: '', userText, body: { messages: [{ role: 'user', content: userText }] } })

  it('matches the summarizer directive of the Claude Code /compact prompt, as a user and as a body pattern', () => {
    const prompt = 'Keep it.\n\nCRITICAL: Respond with TEXT ONLY. Do NOT call any tools.'
    expect(matchesRequest({ user: CLAUDE_SUMMARIZER_PATTERN }, request(prompt))).toBe(true)
    expect(matchesRequest({ body: CLAUDE_SUMMARIZER_PATTERN }, request(prompt))).toBe(true)
  })

  it('does not match an ordinary turn', () => {
    expect(matchesRequest({ user: CLAUDE_SUMMARIZER_PATTERN }, request('Respond with the next step.'))).toBe(false)
  })
})

describe('compactionSummaryText', () => {
  it('puts the whole summary into the envelope of the provider', () => {
    expect(compactionSummaryText({ summaryEnvelope: summary => `<summary>${summary}</summary>` })).toBe(`<summary>${MANUAL_COMPACTION_SUMMARY}</summary>`)
  })

  it('refuses an envelope that drops the summary marker', () => {
    expect(() => compactionSummaryText({ summaryEnvelope: () => 'An empty summary.' })).toThrow('must keep the whole summary')
  })
})

describe('exerciseNativeCompaction', () => {
  const queued: NativeCompactionOptions = { summary: { route: 'queued' } }

  it('reads every request from the queue index after an earlier turn, and returns the queued summarizer request', async () => {
    const { script } = simulatedAgent({ earlierSteps: 2 })
    const result = await exerciseNativeCompaction(context(script), queued)
    expect(result.summaryRequest?.stepIndex).toBe(5)
    expect(result.nextRequest.stepIndex).toBe(6)
    expect(JSON.stringify(result.nextRequest.body)).toContain(MANUAL_COMPACTION_MARKER)
  })

  it('proves a fallback summarizer by its request marker', async () => {
    const { script } = simulatedAgent({ summarizerText: 'NATIVE SUMMARIZER PROMPT' })
    const result = await exerciseNativeCompaction(context(script), { summary: { route: 'fallback', requestMarker: 'NATIVE SUMMARIZER PROMPT' } })
    expect(result.summaryRequest).toBeUndefined()
  })

  it('fails for a fallback request without the marker of the native summarizer', async () => {
    const { script } = simulatedAgent({ summarizerText: 'Another prompt.' })
    await expect(exerciseNativeCompaction(context(script), { summary: { route: 'fallback', requestMarker: 'NATIVE SUMMARIZER PROMPT' } }))
      .rejects
      .toThrow('a native summary request reached the model')
  })

  it('proves a fallback summarizer that the proof cannot read by its completion message', async () => {
    const { script } = simulatedAgent({ completion: 'Context compacted' })
    await exerciseNativeCompaction(context(script), { summary: { route: 'fallback', completionText: 'Context compacted' } })
    const silent = simulatedAgent()
    await expect(exerciseNativeCompaction(context(silent.script), { summary: { route: 'fallback', completionText: 'Context compacted' } }))
      .rejects
      .toThrow('Context compacted')
  })

  it('proves a rule summarizer by its match', async () => {
    const { script } = simulatedAgent({ summarizerText: 'RULE SUMMARIZER PROMPT' })
    await exerciseNativeCompaction(context(script), { summary: { route: 'rule', when: { user: 'RULE SUMMARIZER' } } })
  })

  it('fails when the old context remains in the next request', async () => {
    const { script } = simulatedAgent({ keepsHistory: true })
    await expect(exerciseNativeCompaction(context(script), queued)).rejects.toThrow(OLDER_CONTEXT_MARKER)
  })

  it('reports the input tokens of each seed turn when the provider compares the history size', async () => {
    const { script } = simulatedAgent()
    const queue = vi.spyOn(script, 'queue')
    await exerciseNativeCompaction(context(script), { ...queued, reportedInputTokens: 6000 })
    expect(queue.mock.calls.slice(0, 3).map(([step]) => step?.usage?.inputTokens)).toEqual([6000, 6500, 7000])
  })

  it('answers through the native text step of the provider', async () => {
    const { script } = simulatedAgent()
    const queue = vi.spyOn(script, 'queue')
    await exerciseNativeCompaction({ ...context(script), textStep: text => ({ text: `NATIVE ${text}` }) }, queued)
    expect(queue.mock.calls[0]?.[0]?.text).toMatch(/^NATIVE OLDER_CONTEXT/)
    expect(queue.mock.calls[3]?.[0]?.text).toBe(MANUAL_COMPACTION_SUMMARY)
  })
})
