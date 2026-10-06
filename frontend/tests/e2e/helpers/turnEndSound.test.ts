import type { Page } from '@playwright/test'
import type { ModelScript } from './modelScriptFixture'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { AgentActivityState, AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { selectedAgentTabId } from './nativeScenario'
import { bashToolCall } from './providerToolCalls'
import { parseSettledReceipt, selectIdleReceipt, sendToolUsingTurn, TOOL_USING_PROMPT } from './turnEndSound'
import { sendMessage } from './ui'

vi.mock('./nativeScenario', () => ({ selectedAgentTabId: vi.fn() }))
vi.mock('./ui', () => ({ sendMessage: vi.fn(async () => {}), setInitialBrowserPref: vi.fn() }))

describe('parseSettledReceipt', () => {
  it('preserves explicit zero and the absence of a count', () => {
    const zero = parseSettledReceipt({ agentId: 'a1', state: AgentActivityState.IDLE, numToolUses: 0 })
    const absent = parseSettledReceipt({ agentId: 'a1', state: AgentActivityState.IDLE })
    expect(zero.numToolUses).toBe(0)
    expect(Object.hasOwn(zero, 'numToolUses')).toBe(true)
    expect(Object.hasOwn(absent, 'numToolUses')).toBe(false)
  })

  it('rejects malformed counts and missing agent or settled state', () => {
    for (const value of [null, {}, { agentId: '', state: AgentActivityState.IDLE }, { agentId: 'a1', state: AgentActivityState.WORKING }])
      expect(() => parseSettledReceipt(value)).toThrow()
    for (const count of [-1, 0.5, null, undefined, Number.NaN, Number.POSITIVE_INFINITY, Number.MAX_SAFE_INTEGER + 1])
      expect(() => parseSettledReceipt({ agentId: 'a1', state: AgentActivityState.IDLE, numToolUses: count })).toThrow('tool count')
  })
})

describe('selectIdleReceipt', () => {
  it('excludes an old idle edge, another agent, and a waiting transition', () => {
    const receipts = [
      { agentId: 'a1', state: AgentActivityState.IDLE, numToolUses: 1 },
      { agentId: 'a2', state: AgentActivityState.IDLE, numToolUses: 0 },
      { agentId: 'a1', state: AgentActivityState.WAITING_FOR_USER },
      { agentId: 'a1', state: AgentActivityState.IDLE, numToolUses: 0 },
    ]
    expect(selectIdleReceipt(receipts, { agentId: 'a1', after: 1 })).toEqual(receipts[3])
    expect(selectIdleReceipt(receipts, { agentId: 'a1', after: 4 })).toBeUndefined()
  })

  it('rejects an invalid cursor or agent ID', () => {
    for (const after of [-1, 0.5, Number.MAX_SAFE_INTEGER + 1])
      expect(() => selectIdleReceipt([], { agentId: 'a1', after })).toThrow('boundary')
    expect(() => selectIdleReceipt([], { agentId: '', after: 0 })).toThrow('boundary')
  })
})

describe('sendToolUsingTurn', () => {
  /** A page whose sound probe already holds `settled` receipts. */
  function probedPage(settled: unknown[]): Page {
    return { evaluate: vi.fn(async () => ({ plays: [], settled })) } as unknown as Page
  }

  /** A script that already holds `queued` steps, with each call in `log`. */
  function recordingScript(log: string[], queued: number): ModelScript {
    return {
      queue: vi.fn(async (...steps: unknown[]) => {
        log.push(`queue ${JSON.stringify(steps)}`)
        return queued
      }),
      prompt: (text: string) => `[marked] ${text}`,
      waitForSteps: vi.fn(async (count?: number) => {
        log.push(`wait ${count}`)
        return {}
      }),
    } as unknown as ModelScript
  }

  beforeEach(() => {
    vi.mocked(selectedAgentTabId).mockReset().mockResolvedValue('agent-1')
    vi.mocked(sendMessage).mockReset().mockResolvedValue(undefined)
  })

  it('scripts the shell call and the answer, sends the marked prompt, and waits for both steps of this turn', async () => {
    const log: string[] = []
    const boundary = await sendToolUsingTurn(probedPage([{}, {}]), recordingScript(log, 3))
    expect(boundary).toEqual({ agentId: 'agent-1', after: 2 })
    expect(log).toEqual([
      `queue ${JSON.stringify([{ toolCalls: [bashToolCall(AgentProvider.CLAUDE_CODE, 'pwd-call', 'pwd')] }, { text: 'The working directory is above.' }])}`,
      'wait 5',
    ])
    expect(vi.mocked(sendMessage).mock.calls[0]?.[1]).toBe(`[marked] ${TOOL_USING_PROMPT}`)
  })

  it('holds the answer for the requested time', async () => {
    const log: string[] = []
    await sendToolUsingTurn(probedPage([]), recordingScript(log, 0), { holdAnswerMs: 2_000 })
    expect(log[0]).toContain('"delayMs":2000')
  })

  it.each([-1, 1.5, Number.NaN])('refuses a hold of %s before it reads the page', async (holdAnswerMs) => {
    const page = probedPage([])
    await expect(sendToolUsingTurn(page, recordingScript([], 0), { holdAnswerMs })).rejects.toThrow(RangeError)
    expect(page.evaluate).not.toHaveBeenCalled()
    expect(selectedAgentTabId).not.toHaveBeenCalled()
  })
})
