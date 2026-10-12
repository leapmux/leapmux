import type { Page } from '@playwright/test'
import type { MockModelRequestRecord, MockModelStep } from './mockModelScript'
import type { ModelScript } from './modelScriptFixture'
import type { ManagedNativeScenarioContext } from './nativeScenario'
import { mkdirSync, mkdtempSync, rmSync, writeFileSync } from 'node:fs'
import { join, resolve } from 'node:path'
import process from 'node:process'
import { create } from '@bufbuild/protobuf'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { AgentActivityState, AgentInfoSchema, AgentProvider, AgentStatus } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { exerciseControlInterrupt, raiseWaitingControl, WAITING_CONTROL_CALL_ID, WAITING_QUESTION, waitingControlSteps } from './controlInterrupt'
import { askUserQuestionToolCall, bashToolCall } from './providerToolCalls'

/** The steps that the scenario takes, in order, and the state that the fakes report. */
const flow = vi.hoisted(() => ({
  events: [] as string[],
  /** The Worker activity that each read of the agent reports, in order. The last one repeats. */
  activities: [] as number[],
  /** The native session that each read of the current agent reports, in order. The last one repeats. */
  sessions: [] as string[],
  /** The answers of the earlier turns, which a native session that keeps its history sends again. */
  answers: [] as string[],
  keepsHistory: true,
  /** The receipt that the browser records for the stop. A stop from WAITING_FOR_USER records none. */
  receipt: undefined as { agentId: string, state: string } | undefined,
  workingDir: '',
  turnEnded: vi.fn<(script: unknown, nextStep: number) => Promise<void>>(),
  nativeEnd: vi.fn<(text: string) => Promise<void>>(),
}))

/** Return the next value of `values`. The last value repeats. */
function next<T>(values: T[]): T {
  const value = values.length > 1 ? values.shift() : values[0]
  if (value === undefined)
    throw new Error('The fake holds no value to report.')
  return value
}

/** A fake locator. The fake `expect` below records its assertions. */
function fake(name: string) {
  return { fake: name, last: () => fake(name) }
}

vi.mock('./ui', () => ({
  sendMessage: async (_page: Page, text: string) => {
    flow.events.push(`send:${text}`)
  },
  waitForControlBanner: async () => {
    flow.events.push('banner')
    return {
      ...fake('banner'),
      getByTestId: (testId: string) => ({
        click: async () => {
          flow.events.push(`click:${testId}`)
        },
      }),
    }
  },
  expectNoControlBanner: async () => {
    flow.events.push('no-banner')
  },
  interruptButton: () => fake('interrupt-button'),
  resumePausedQueue: async () => {
    flow.events.push('resume-queue')
  },
}))

vi.mock('./nativeConversation', () => ({
  // A native session that keeps its history sends the earlier answers with each new prompt.
  sendNativeAnswer: async (_context: unknown, prompt: string, answer: string): Promise<MockModelRequestRecord> => {
    flow.events.push(`answer:${prompt}=>${answer}`)
    const history = flow.keepsHistory ? flow.answers.join('\n') : ''
    flow.answers.push(answer)
    return { protocol: 'openai-chat-completions', path: '/v1/chat/completions', body: { messages: [history, prompt] } }
  },
}))

vi.mock('./nativeScenario', async importOriginal => ({
  ...await importOriginal<typeof import('./nativeScenario')>(),
  currentNativeAgent: async () => create(AgentInfoSchema, {
    id: 'waiting-agent',
    status: AgentStatus.ACTIVE,
    agentSessionId: next(flow.sessions),
    workingDir: flow.workingDir,
  }),
  nativeAgentById: async () => {
    const activity = next(flow.activities)
    flow.events.push(`worker:${AgentActivityState[activity]}`)
    return create(AgentInfoSchema, { id: 'waiting-agent', activityState: activity })
  },
}))

vi.mock('./nativeInputQueueIdle', () => ({
  readNativeInputQueue: async () => {
    flow.events.push('queue:paused')
    return { agentId: 'waiting-agent', paused: true, items: [] }
  },
}))

vi.mock('./turnEndSound', () => ({
  observeSettledReceipts: async () => {
    flow.events.push('observe-receipts')
    return 4
  },
  currentIdleReceipt: async (_page: Page, boundary: { agentId: string, after: number }) => {
    flow.events.push(`receipt:${boundary.agentId}:${boundary.after}`)
    return flow.receipt
  },
}))

vi.mock('./modelScriptFixture', () => ({
  expectTurnEndedAfter: async (script: unknown, nextStep: number) => {
    flow.events.push(`turn-ended:${nextStep}`)
    await flow.turnEnded(script, nextStep)
  },
}))

// One attempt, so a failed check fails the test at once. A unit test has no test deadline, so the real wait would
// retry the failure with no limit.
vi.mock('./retryUntilPass', () => ({
  retryUntilPass: async <T>(attempt: () => T | Promise<T>) => attempt(),
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
            flow.events.push(`${name} holds:${text}`)
            if (name === '[data-testid="result-divider"]:visible')
              await flow.nativeEnd(text)
          },
          toHaveCount: async (count: number) => {
            flow.events.push(`${name} count:${count}`)
          },
        }
      }
      return expect(value, message)
    },
  }
})

/** A script whose queue starts at `offset`, as after the first turn of the scenario. */
function fakeScript(offset: number) {
  const queued: MockModelStep[] = []
  const script = {
    prompt: (text: string) => `MARKED:${text}`,
    queue: vi.fn(async (...steps: MockModelStep[]) => {
      const index = offset + queued.length
      queued.push(...steps)
      return index
    }),
    waitForSteps: vi.fn(async (count: number) => {
      flow.events.push(`steps:${count}`)
    }),
  }
  return { script, queued }
}

function context(script: ReturnType<typeof fakeScript>['script'], provider = AgentProvider.CODEX): ManagedNativeScenarioContext {
  return {
    // The page serves only the locator of the thinking indicator.
    page: { locator: (selector: string) => fake(selector) } as unknown as Page,
    modelScript: script as unknown as ModelScript,
    provider,
    providerAgent: { provider, prefix: 'control-interrupt-unit' },
    workspaceId: 'control-interrupt-workspace',
    leapmuxServer: { hubUrl: 'http://unused.invalid', adminToken: 'controlled-token', workerId: 'control-interrupt-worker' },
  }
}

/** The marker of the run, read from the first prompt of the scenario. */
function runMarker(): string {
  const marker = flow.events.find(event => event.startsWith('answer:Keep CONTROLCONTEXT'))?.match(/CONTROLCONTEXT(\w+) /)?.[1]
  if (!marker)
    throw new Error('The scenario sent no first turn with a marker.')
  return marker
}

/** The directory where a test that creates real directories keeps them. Each such test removes its own. */
const SCRATCH_ROOT = resolve(process.cwd(), '../.tmp')

describe('raiseWaitingControl', () => {
  it('raises the waiting question through the native question tool of the provider', () => {
    expect(raiseWaitingControl(AgentProvider.CODEX, 'question', '', 'abc123')).toEqual({
      toolCall: askUserQuestionToolCall(AgentProvider.CODEX, WAITING_CONTROL_CALL_ID, [WAITING_QUESTION]),
      bannerText: WAITING_QUESTION.question,
    })
  })

  it('raises a permission for a command that writes a marked file in the working directory', () => {
    expect(raiseWaitingControl(AgentProvider.GOOSE, 'permission', '/work/agent', 'abc123')).toEqual({
      toolCall: bashToolCall(AgentProvider.GOOSE, WAITING_CONTROL_CALL_ID, 'printf WAITINGCONTROL > waiting-control-abc123.txt'),
      bannerText: 'waiting-control-abc123.txt',
      guardedFile: '/work/agent/waiting-control-abc123.txt',
    })
  })

  it.each(['', 'relative/agent'])('refuses a permission in the working directory %j, which is not absolute', (workingDir) => {
    expect(() => raiseWaitingControl(AgentProvider.GOOSE, 'permission', workingDir, 'abc123')).toThrow('absolute working directory')
  })

  it.each(['', 'a b', 'a/b', 'a;rm'])('refuses the marker %j, which a command or a file name cannot hold as it is', (marker) => {
    expect(() => raiseWaitingControl(AgentProvider.GOOSE, 'question', '/work/agent', marker)).toThrow('marker of word characters')
  })
})

describe('waitingControlSteps', () => {
  const control = raiseWaitingControl(AgentProvider.CODEWHALE, 'question', '', 'abc123')

  it('raises the control in one step', () => {
    expect(waitingControlSteps(control)).toEqual([{ toolCalls: [control.toolCall] }])
  })
})

describe('exerciseControlInterrupt', () => {
  const directories: string[] = []

  beforeEach(() => {
    flow.events.length = 0
    flow.activities = [AgentActivityState.WAITING_FOR_USER, AgentActivityState.IDLE]
    flow.sessions = ['native-session-1']
    flow.answers = []
    flow.keepsHistory = true
    flow.receipt = undefined
    flow.turnEnded.mockReset().mockResolvedValue(undefined)
    flow.nativeEnd.mockReset().mockResolvedValue(undefined)
    mkdirSync(SCRATCH_ROOT, { recursive: true })
    flow.workingDir = mkdtempSync(join(SCRATCH_ROOT, 'control-interrupt-'))
    directories.push(flow.workingDir)
  })

  afterEach(() => {
    for (const directory of directories.splice(0))
      rmSync(directory, { recursive: true, force: true })
  })

  it('waits for the native interruption divider before accepting the stop proof', async () => {
    const { script } = fakeScript(1)
    await exerciseControlInterrupt(context(script), { control: 'question' })
    expect(flow.nativeEnd).toHaveBeenCalledExactlyOnceWith('interrupted')
    expect(flow.events.indexOf('[data-testid="result-divider"]:visible holds:interrupted'))
      .toBeLessThan(flow.events.indexOf('turn-ended:2'))
  })

  it('interrupts a waiting question from its banner, then proves the idle agent, the paused queue and the kept session in order', async () => {
    const { script, queued } = fakeScript(1)
    const prepare = vi.fn(async () => {
      flow.events.push('prepare')
    })
    await exerciseControlInterrupt(context(script), { control: 'question', prepare })
    const marker = runMarker()
    expect(queued).toEqual([{ toolCalls: [askUserQuestionToolCall(AgentProvider.CODEX, WAITING_CONTROL_CALL_ID, [WAITING_QUESTION])] }])
    expect(flow.turnEnded).toHaveBeenCalledExactlyOnceWith(script, 2)
    expect(flow.events).toEqual([
      'prepare',
      `answer:Keep CONTROLCONTEXT${marker} for this session.=>CONTROLANSWER${marker}`,
      'send:MARKED:Wait on the scripted control.',
      'steps:2',
      'banner',
      `banner holds:${WAITING_QUESTION.question}`,
      'worker:WAITING_FOR_USER',
      'observe-receipts',
      'click:control-interrupt',
      'worker:IDLE',
      'no-banner',
      '[data-testid="thinking-indicator"]:visible count:0',
      'interrupt-button count:0',
      '[data-testid="result-divider"]:visible holds:interrupted',
      'receipt:waiting-agent:4',
      'turn-ended:2',
      'queue:paused',
      'resume-queue',
      `answer:Continue after the withdrawn control.=>AFTERCONTROL${marker}`,
    ])
  })

  it('waits for the raising call, and ends the turn after it', async () => {
    const { script, queued } = fakeScript(1)
    await exerciseControlInterrupt(context(script, AgentProvider.CODEWHALE), { control: 'question' })
    const control = raiseWaitingControl(AgentProvider.CODEWHALE, 'question', '', 'abc123')
    expect(queued).toEqual(waitingControlSteps(control))
    expect(flow.events).toContain('steps:2')
    expect(flow.turnEnded).toHaveBeenCalledExactlyOnceWith(script, 2)
  })

  it('raises a permission whose banner states the file of its command, and passes while the file stays absent', async () => {
    const { script, queued } = fakeScript(1)
    await exerciseControlInterrupt(context(script, AgentProvider.GOOSE), { control: 'permission' })
    const marker = runMarker()
    expect(queued).toEqual([{ toolCalls: [bashToolCall(AgentProvider.GOOSE, WAITING_CONTROL_CALL_ID, `printf WAITINGCONTROL > waiting-control-${marker}.txt`)] }])
    expect(flow.events).toContain(`banner holds:waiting-control-${marker}.txt`)
  })

  it('fails when the withdrawn permission ran its command', async () => {
    const { script } = fakeScript(1)
    flow.turnEnded.mockImplementation(async () => {
      writeFileSync(join(flow.workingDir, `waiting-control-${runMarker()}.txt`), 'WAITINGCONTROL')
    })
    await expect(exerciseControlInterrupt(context(script, AgentProvider.GOOSE), { control: 'permission' })).rejects.toThrow('the withdrawn permission never ran its command')
  })

  it('fails before the interrupt while the Worker reports no wait on the control', async () => {
    flow.activities = [AgentActivityState.IDLE]
    const { script } = fakeScript(1)
    await expect(exerciseControlInterrupt(context(script), { control: 'question' })).rejects.toThrow('the Worker holds the agent waiting for the user')
    expect(flow.events).not.toContain('click:control-interrupt')
  })

  it('fails while the Worker reports the interrupted agent at work', async () => {
    flow.activities = [AgentActivityState.WAITING_FOR_USER, AgentActivityState.WORKING]
    const { script } = fakeScript(1)
    await expect(exerciseControlInterrupt(context(script), { control: 'question' })).rejects.toThrow('the Worker reports the interrupted agent as idle')
    expect(flow.events).not.toContain('no-banner')
  })

  it('fails when the stop recorded an idle receipt, as a settle edge does', async () => {
    flow.receipt = { agentId: 'waiting-agent', state: 'idle' }
    const { script } = fakeScript(1)
    await expect(exerciseControlInterrupt(context(script), { control: 'question' })).rejects.toThrow('a stopped waiting agent records no idle receipt')
    expect(flow.events).not.toContain('turn-ended:2')
  })

  it('fails when the agent sent a model request after the interrupt, and resumes no queue', async () => {
    const unexpected = new Error('the agent sent no request that the script did not expect')
    flow.turnEnded.mockRejectedValue(unexpected)
    const { script } = fakeScript(1)
    await expect(exerciseControlInterrupt(context(script), { control: 'question' })).rejects.toBe(unexpected)
    expect(flow.events).not.toContain('resume-queue')
  })

  it('fails when the turn after the interrupt no longer reads the turn before it', async () => {
    flow.keepsHistory = false
    const { script } = fakeScript(1)
    await expect(exerciseControlInterrupt(context(script), { control: 'question' })).rejects.toThrow('CONTROLANSWER')
  })

  it('fails when the turn after the interrupt runs in another native session', async () => {
    flow.sessions = ['native-session-1', 'native-session-2']
    const { script } = fakeScript(1)
    await expect(exerciseControlInterrupt(context(script), { control: 'question' })).rejects.toThrow('native-session-1')
  })
})
