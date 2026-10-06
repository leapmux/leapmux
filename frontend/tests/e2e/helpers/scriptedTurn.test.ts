import type { Page } from '@playwright/test'
import type { ModelScript } from './modelScriptFixture'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { ARITHMETIC_TURN, sayExactly, sendScriptedTurn, wholeText } from './scriptedTurn'
import { expectAssistantAnswer, sendMessage, waitForAgentIdle } from './ui'

vi.mock('./ui', () => ({
  ARITHMETIC_ANSWER_TEXT: '6912',
  ARITHMETIC_PROMPT: 'What is 1234 + 5678? Reply with just the number.',
  expectAssistantAnswer: vi.fn(async () => {}),
  sendMessage: vi.fn(async () => {}),
  waitForAgentIdle: vi.fn(async () => {}),
}))

const page = {} as Page

/** A script that already holds `queued` answers. Each call appends to `log`. */
function recordingScript(log: string[], queued: number): ModelScript {
  return {
    queue: vi.fn(async (...steps: Array<{ text?: string }>) => {
      log.push(`queue ${steps.map(step => step.text).join(',')}`)
      return queued
    }),
    prompt: (text: string) => `[marked] ${text}`,
    waitForSteps: vi.fn(async (count?: number) => {
      log.push(`wait ${count}`)
      return { steps: [] }
    }),
  } as unknown as ModelScript
}

beforeEach(() => {
  vi.mocked(sendMessage).mockReset()
  vi.mocked(expectAssistantAnswer).mockReset()
  vi.mocked(waitForAgentIdle).mockReset()
})

describe('sendScriptedTurn', () => {
  it('queues the answer, sends the marked prompt, waits for that step, checks the answer, and waits for idle', async () => {
    const log: string[] = []
    vi.mocked(sendMessage).mockImplementation(async (_page, text, entry) => {
      log.push(`send ${text} as ${entry}`)
    })
    vi.mocked(expectAssistantAnswer).mockImplementation(async (_page, options) => {
      log.push(`answer ${String(options?.answer)}`)
    })
    vi.mocked(waitForAgentIdle).mockImplementation(async () => {
      log.push('idle')
    })
    const step = await sendScriptedTurn(page, recordingScript(log, 3), { prompt: 'Say hi.', answer: 'Hi.', entry: 'insert' })
    expect(step).toBe(3)
    expect(log).toEqual([
      'queue Hi.',
      'send [marked] Say hi. as insert',
      // The step index of this answer is 3, so the wait is for four consumed steps, not for every queued step.
      'wait 4',
      `answer ${String(wholeText('Hi.'))}`,
      'idle',
    ])
  })

  it('sends the arithmetic turn by default, typed', async () => {
    const log: string[] = []
    await sendScriptedTurn(page, recordingScript(log, 0))
    expect(log).toEqual(['queue 6912', 'wait 1'])
    expect(sendMessage).toHaveBeenCalledWith(page, `[marked] ${ARITHMETIC_TURN.prompt}`, undefined)
  })

  it('refuses an empty answer before it queues anything', async () => {
    const log: string[] = []
    await expect(sendScriptedTurn(page, recordingScript(log, 0), { prompt: 'Say nothing.', answer: '  ' }))
      .rejects
      .toThrow('needs an answer')
    expect(log).toEqual([])
    expect(sendMessage).not.toHaveBeenCalled()
  })

  it('does not check the answer when the step never arrives', async () => {
    const script = recordingScript([], 0)
    vi.mocked(script.waitForSteps).mockRejectedValue(new Error('The script is incomplete.'))
    await expect(sendScriptedTurn(page, script)).rejects.toThrow('The script is incomplete.')
    expect(expectAssistantAnswer).not.toHaveBeenCalled()
    expect(waitForAgentIdle).not.toHaveBeenCalled()
  })
})

describe('sayExactly', () => {
  it('asks for the text and answers with the same text', () => {
    expect(sayExactly('Hello world')).toEqual({ prompt: 'Say exactly: Hello world', answer: 'Hello world' })
  })
})

describe('wholeText', () => {
  it('matches the text with no word character at either end', () => {
    expect(wholeText('ok').test('ok')).toBe(true)
    expect(wholeText('ok').test('The answer is ok.')).toBe(true)
    expect(wholeText('ok').test('1.2k tokens')).toBe(false)
    expect(wholeText('ok').test('okay')).toBe(false)
  })

  it('matches syntax characters literally', () => {
    expect(wholeText('Hello.').test('Hello.')).toBe(true)
    expect(wholeText('Hello.').test('Hellox')).toBe(false)
    expect(wholeText('a (b)').test('say a (b) now')).toBe(true)
  })

  it('matches an answer that ends in punctuation at the end of the text', () => {
    expect(wholeText('Noted.').test('Noted.')).toBe(true)
  })
})
