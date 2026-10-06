import type { Page } from '@playwright/test'
import type { MockModelStep } from './mockModelScript'
import type { ModelScript } from './modelScriptFixture'
import { join } from 'node:path'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { ENTER_PLAN_PROMPT, enterAndExitPlanMode, enterPlanMode, EXIT_PLAN_PROMPT, exitPlanMode, planText } from './plan-mode'

const browser = vi.hoisted(() => ({ events: [] as string[], banner: { banner: true } }))

vi.mock('./server', () => ({ getGlobalState: () => ({ agentEnv: { HOME: '/private/home' } }) }))
vi.mock('./ui', () => ({
  sendMessage: async (_page: Page, text: string) => {
    browser.events.push(`send:${text}`)
  },
  waitForAgentIdle: async () => {
    browser.events.push('idle')
  },
  waitForControlBanner: async () => {
    browser.events.push('banner')
    return browser.banner
  },
}))

/** A model script that records each queued step, each fallback, and each wait. */
function recordingScript() {
  const queued: MockModelStep[] = []
  const fallbacks: MockModelStep[] = []
  const script = {
    prompt: (text: string) => `${text}\nMARKER`,
    queue: vi.fn(async (...steps: MockModelStep[]) => {
      queued.push(...steps)
      return 0
    }),
    fallback: vi.fn(async (step: MockModelStep) => {
      fallbacks.push(step)
    }),
    waitForSteps: vi.fn(async () => {
      browser.events.push('steps')
    }),
  } as unknown as ModelScript
  return { script, queued, fallbacks }
}

beforeEach(() => {
  browser.events.length = 0
})

describe('enterPlanMode', () => {
  it('scripts the enter call, the plan write, and the answer in the tool vocabulary of the context provider', async () => {
    const { script, queued, fallbacks } = recordingScript()
    await enterPlanMode({ page: {} as Page, modelScript: script, provider: AgentProvider.GROK_BUILD }, { testId: 'unit' })
    expect(queued.map(step => step.toolCalls?.map(call => call.name) ?? step.text)).toEqual([['enter_plan_mode'], ['write'], 'I am in plan mode and the plan is written.'])
    expect(queued[1]?.toolCalls?.[0]?.arguments).toEqual({ file_path: join('/private/home', '.claude', 'plans', 'dummy-plan-unit.md'), content: `${planText('unit')}\nMARKER` })
    expect(fallbacks).toEqual([{ text: 'Working through the plan.' }])
    expect(browser.events).toEqual([`send:${ENTER_PLAN_PROMPT}\nMARKER`, 'steps', 'idle'])
  })
})

describe('exitPlanMode', () => {
  it('scripts the exit call of the context provider with the marked plan, and returns the banner it raises', async () => {
    const { script, queued } = recordingScript()
    const banner = await exitPlanMode({ page: {} as Page, modelScript: script, provider: AgentProvider.CLAUDE_CODE })
    expect(banner).toBe(browser.banner)
    expect(queued).toEqual([{ toolCalls: [{ id: 'exit-plan', name: 'ExitPlanMode', arguments: { plan: `${planText()}\nMARKER` } }] }])
    expect(browser.events).toEqual([`send:${EXIT_PLAN_PROMPT}\nMARKER`, 'steps', 'banner'])
  })
})

describe('enterAndExitPlanMode', () => {
  it('enters and then leaves plan mode with one plan ID', async () => {
    const { script, queued } = recordingScript()
    await enterAndExitPlanMode({ page: {} as Page, modelScript: script, provider: AgentProvider.CLAUDE_CODE }, { testId: 'both' })
    expect(queued.map(step => step.toolCalls?.[0]?.name ?? step.text)).toEqual(['EnterPlanMode', 'Write', 'I am in plan mode and the plan is written.', 'ExitPlanMode'])
    expect(queued[3]?.toolCalls?.[0]?.arguments).toEqual({ plan: `${planText('both')}\nMARKER` })
  })
})
