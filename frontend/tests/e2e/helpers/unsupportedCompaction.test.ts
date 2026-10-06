import type { Page } from '@playwright/test'
import type { MockModelRequestRecord, MockModelStep } from './mockModelScript'
import type { ModelScript } from './modelScriptFixture'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { exerciseCompactAsModelText } from './unsupportedCompaction'

/** A simulated native agent: what it sent, what the transcript shows, and whether a notice row exists. */
const agent = vi.hoisted(() => ({
  send: (_text: string) => {},
  transcript: [] as string[],
  sent: [] as string[],
  notice: false,
}))

vi.mock('./ui', () => ({
  sendMessage: async (_page: Page, text: string) => {
    agent.sent.push(text)
    agent.send(text)
  },
  waitForAgentIdle: async () => {},
  assistantBubbles: () => ({ filter: ({ hasText }: { hasText: string }) => ({ first: () => ({ fakeText: hasText }) }) }),
  userBubbles: () => ({ filter: ({ hasText }: { hasText: string }) => ({ first: () => ({ fakeText: hasText }) }) }),
}))

vi.mock('./compaction', () => ({ compactionNoticeRow: () => ({ fakeNotice: true }) }))

vi.mock('@playwright/test', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@playwright/test')>()
  return {
    ...actual,
    expect: (value: unknown, message?: string) => {
      if (typeof value === 'object' && value !== null && 'fakeText' in value && typeof value.fakeText === 'string') {
        const text = value.fakeText
        return { toBeVisible: async () => expect(agent.transcript.join('\n'), `the transcript shows ${text}`).toContain(text) }
      }
      if (typeof value === 'object' && value !== null && 'fakeNotice' in value)
        return { toHaveCount: async (count: number) => expect(agent.notice ? 1 : 0, 'the compaction notice rows').toBe(count) }
      return expect(value, message)
    },
  }
})

interface SimulatedAgentOptions {
  /** Steps that an earlier turn of the test consumed. */
  earlierSteps?: number
  /** The agent runs `/compact` as a native command: it sends a summarizer prompt and draws a notice. */
  nativeCommand?: boolean
  /** The agent starts a new conversation for the command. */
  dropsHistory?: boolean
}

/** A model script and a native agent that sends each message to the model with its whole history. */
function simulatedAgent(options: SimulatedAgentOptions = {}): ModelScript {
  const steps: MockModelStep[] = Array.from({ length: options.earlierSteps ?? 0 }, () => ({ text: 'An earlier turn.' }))
  const requests: MockModelRequestRecord[] = []
  let nextStep = steps.length
  let history: { role: string, content: string }[] = []
  agent.transcript = []
  agent.sent = []
  agent.notice = false
  agent.send = (text) => {
    if (options.dropsHistory && text.startsWith('/compact'))
      history = []
    const userText = options.nativeCommand && text.startsWith('/compact') ? 'Summarize the conversation.' : text
    history.push({ role: 'user', content: userText })
    requests.push({ stepIndex: nextStep, protocol: 'openai-chat-completions', body: { messages: [...history] } } as unknown as MockModelRequestRecord)
    const reply = steps[nextStep++]?.text ?? ''
    history.push({ role: 'assistant', content: reply })
    agent.transcript.push(reply)
    if (options.nativeCommand && text.startsWith('/compact'))
      agent.notice = true
  }
  return {
    prompt: (text: string) => `${text}\n\nMARKER`,
    queue: async (...queued: MockModelStep[]) => {
      const index = steps.length
      steps.push(...queued)
      return index
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
  } as unknown as ModelScript
}

function context(script: ModelScript) {
  return { page: {} as Page, modelScript: script, provider: AgentProvider.CLINE }
}

beforeEach(() => {
  agent.transcript = []
  agent.sent = []
  agent.notice = false
})

describe('exerciseCompactAsModelText', () => {
  it('reads the command request from its queue index after an earlier turn, with the earlier turn in its context', async () => {
    const result = await exerciseCompactAsModelText(context(simulatedAgent({ earlierSteps: 3 })))
    expect(result.first.stepIndex).toBe(3)
    expect(result.request.stepIndex).toBe(4)
    expect(agent.sent.at(-1)).toBe('/compact')
    expect(JSON.stringify(result.request.body)).toContain(result.prompt)
    expect(JSON.stringify(result.request.body)).toContain(result.answer)
  })

  it('sends the command with the scenario marker when the provider needs it', async () => {
    await exerciseCompactAsModelText(context(simulatedAgent()), { markCommand: true })
    expect(agent.sent.at(-1)).toBe('/compact\n\nMARKER')
  })

  it('reads the last user text through the reader of the provider', async () => {
    const reader = vi.fn(() => 'the provider prompt holds /compact')
    await exerciseCompactAsModelText(context(simulatedAgent()), { lastUserText: reader })
    expect(reader).toHaveBeenCalledTimes(1)
  })

  it('fails when the command reaches the model as another text', async () => {
    await expect(exerciseCompactAsModelText(context(simulatedAgent({ nativeCommand: true }))))
      .rejects
      .toThrow('the last user text of the command request')
  })

  it('fails when the command request lost the earlier turn', async () => {
    await expect(exerciseCompactAsModelText(context(simulatedAgent({ dropsHistory: true })))).rejects.toThrow(/PROMPTCOMPACTTEXT/)
  })

  it('fails when the transcript draws a compaction notice', async () => {
    const script = simulatedAgent()
    const send = agent.send
    agent.send = (text) => {
      send(text)
      if (text === '/compact')
        agent.notice = true
    }
    await expect(exerciseCompactAsModelText(context(script))).rejects.toThrow('the compaction notice rows')
  })
})
