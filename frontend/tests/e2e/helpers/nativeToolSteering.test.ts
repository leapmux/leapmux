import type { Page } from '@playwright/test'
import type { MockModelRequestRecord, MockModelStep } from './mockModelScript'
import type { ModelScript } from './modelScriptFixture'
import type { ManagedNativeScenarioContext } from './nativeScenario'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { AgentActivityState, AgentInputState, AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { exerciseQueuedTurnWithoutSteering, exerciseSteerAfterTool, exerciseSteerBeforeTool } from './nativeToolSteering'
import { bashToolCall } from './providerToolCalls'
import { startWaitLimitForTests } from './testDeadline'

/** The browser, Worker and output steps that a scenario takes, in order, and the state of the doubles. */
const harness = vi.hoisted(() => ({
  events: [] as string[],
  sent: [] as string[],
  agent: { id: 'native-agent', workingDir: '/native/work', supportsSteering: false, supportsPreemption: true },
  queue: [] as { id: string, text: string, state: number, canSteer: boolean, canPreempt: boolean }[],
  /** The Worker queue state of the next sent message, as the held turn leaves it. */
  queueNextSend: undefined as { state: number, canSteer: boolean, canPreempt: boolean } | undefined,
  steerFailure: undefined as Error | undefined,
}))

/** A fake locator that the mocked `expect` recognizes. Its name states the element. */
interface FakeLocator {
  fake: string
  filter: () => FakeLocator
  first: () => FakeLocator
  getByRole: (role: string, options: { name: string }) => FakeLocator
}

function fake(name: string): FakeLocator {
  return {
    fake: name,
    filter: () => fake(name),
    first: () => fake(name),
    getByRole: (_role, options) => fake(`${name} ${options.name}`),
  }
}

vi.mock('./ui', () => ({
  sendMessage: async (_page: Page, text: string) => {
    harness.sent.push(text)
    harness.events.push(`send:${text}`)
    if (harness.queueNextSend) {
      harness.queue = [{ id: 'queued-input', text, ...harness.queueNextSend }]
      harness.queueNextSend = undefined
    }
  },
  waitForAgentIdle: async () => {
    harness.events.push('idle')
  },
  waitForControlBanner: async () => {
    harness.events.push('banner')
    return fake('banner')
  },
  answerControl: async (_page: Page, decision: string) => {
    harness.events.push(`answer:${decision}`)
  },
  assistantBubbles: () => fake('answer'),
  interruptButton: () => fake('[data-testid="interrupt-button"]:visible'),
  messageContents: () => fake('content'),
  userBubbles: () => fake('user'),
}))

vi.mock('./steer', () => ({
  steerQueuedInput: async (_page: Page, input: { message: string }) => {
    if (harness.steerFailure)
      throw harness.steerFailure
    harness.sent.push(input.message)
    harness.events.push(`steer:${input.message}`)
  },
  expectSteeredReply: async (_page: Page, reply: string, at: string) => {
    harness.events.push(`steered reply:${reply}:${at}`)
  },
  queuedInputRow: () => fake('queued row'),
}))

vi.mock('./nativeScenario', async (importOriginal) => {
  const actual = await importOriginal<typeof import('./nativeScenario')>()
  return {
    ...actual,
    currentNativeAgent: async () => harness.agent,
    nativeAgentById: async () => ({ ...harness.agent, activityState: AgentActivityState.WORKING }),
  }
})

vi.mock('./api', () => ({
  getTestChannel: async () => ({
    callWorker: async (_workerId: string, method: string) => {
      if (method === 'ListAgentInputQueue')
        return { snapshot: { items: harness.queue } }
      harness.events.push(`worker:${method}`)
      throw harness.steerFailure ?? Object.assign(new Error('agent provider does not support steering'), { source: 'rpc', code: 9 })
    },
  }),
}))

vi.mock('./toolOutputControl', () => ({
  createToolOutputControl: () => ({
    command: 'node held-output.cjs',
    firstMarker: 'NATIVEFIRSTMARKER',
    secondMarker: 'NATIVESECONDMARKER',
    firstLiveTail: 'xxxx',
    secondLiveTail: 'yyyy-second-tail',
    waitForFirstOutput: async () => {
      harness.events.push('first output')
    },
    waitForSecondOutput: async () => {
      harness.events.push('second output')
    },
    releaseFirstOutput: async () => {
      harness.events.push('release first')
    },
    releaseFinalOutput: async () => {
      harness.events.push('release final')
    },
  }),
}))

vi.mock('@playwright/test', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@playwright/test')>()
  const fakeExpect = (value: unknown, message?: string) => {
    if (typeof value === 'object' && value !== null && 'fake' in value && typeof value.fake === 'string') {
      const name = value.fake
      return {
        toBeVisible: async () => {
          harness.events.push(`${name} visible`)
        },
        toHaveCount: async (count: number) => {
          harness.events.push(`${name} count:${count}`)
        },
        toContainText: async (text: string) => {
          harness.events.push(`${name} holds:${text}`)
        },
      }
    }
    return expect(value, message)
  }
  return { ...actual, expect: Object.assign(fakeExpect, { poll: (read: () => unknown, options?: { message?: string }) => expect.poll(read, options) }) }
})

/** A script whose queue starts at `offset`, as after an earlier turn. `requestAt` builds each request from what the test sent. */
function fakeScript(offset: number, request: (stepIndex: number, queued: readonly MockModelStep[]) => MockModelRequestRecord) {
  const queued: MockModelStep[] = []
  const script = {
    prompt: (text: string) => `MARKED:${text}`,
    queue: vi.fn(async (...steps: MockModelStep[]) => {
      const index = offset + queued.length
      queued.push(...steps)
      return index
    }),
    waitForGate: vi.fn(async (gate: string) => {
      harness.events.push(`gate held:${gate.split('-').slice(0, -1).join('-')}`)
    }),
    releaseGate: vi.fn(async () => {
      harness.events.push('gate released')
      harness.queue = []
    }),
    releaseGateIfHeld: vi.fn(async () => false),
    waitForSteps: vi.fn(async (count: number) => {
      harness.events.push(`steps:${count}`)
    }),
    status: vi.fn(async () => ({ requests: [] })),
    requestAt: vi.fn(async (stepIndex: number) => {
      harness.events.push(`request:${stepIndex}`)
      return request(stepIndex, queued)
    }),
  }
  return { script, queued }
}

function context(script: ReturnType<typeof fakeScript>['script']): ManagedNativeScenarioContext {
  return {
    page: { locator: (selector: string) => fake(selector) } as unknown as Page,
    modelScript: script as unknown as ModelScript,
    provider: AgentProvider.OPENCODE,
    workspaceId: 'steer-workspace',
    leapmuxServer: { hubUrl: 'http://unused.invalid', adminToken: 'unused-token', workerId: 'unused-worker' },
  }
}

/** A Chat Completions request whose body holds `texts`. */
function bodyRequest(stepIndex: number, ...texts: string[]): MockModelRequestRecord {
  return { protocol: 'openai-chat-completions', path: '/v1/chat/completions', stepIndex, body: { messages: texts.map(content => ({ role: 'user', content })) } }
}

beforeEach(() => {
  harness.events.length = 0
  harness.sent.length = 0
  harness.agent = { id: 'native-agent', workingDir: '/native/work', supportsSteering: false, supportsPreemption: true }
  harness.queue = []
  harness.queueNextSend = undefined
  harness.steerFailure = undefined
})

describe('exerciseSteerBeforeTool', () => {
  const steering = 'Also include the word STEEREDWORD in your reply.'

  it('steers while the gate holds the tool step and returns the request after the tool step', async () => {
    const { script, queued } = fakeScript(2, stepIndex => bodyRequest(stepIndex, steering))
    const request = await exerciseSteerBeforeTool(context(script))
    expect(queued).toHaveLength(2)
    expect(queued[0]?.toolCalls).toEqual([bashToolCall(AgentProvider.OPENCODE, 'steer-tool', 'printf provider-steer-ready')])
    expect(queued[0]?.gate).toMatch(/^native-steer-before-tool-[0-9a-f]{32}$/)
    expect(queued[1]).toEqual({ text: 'The turn ended with STEEREDWORD.' })
    expect(harness.events).toEqual([
      'send:MARKED:Run the scripted shell command, then reply.',
      'gate held:native-steer-before-tool',
      `steer:${steering}`,
      'gate released',
      'steps:4',
      'idle',
      'request:3',
      'steered reply:STEEREDWORD:last',
      '[data-testid="result-divider"]:visible count:1',
    ])
    expect(request).toEqual(bodyRequest(3, steering))
  })

  it('allows the shell command through its banner before the turn ends, and counts the stated dividers', async () => {
    const { script } = fakeScript(0, stepIndex => bodyRequest(stepIndex, steering))
    await exerciseSteerBeforeTool(context(script), { approveTool: true, resultDividers: 2 })
    expect(harness.events.slice(3, 7)).toEqual(['gate released', 'banner', 'banner holds:printf provider-steer-ready', 'answer:allow'])
    expect(harness.events.at(-1)).toBe('[data-testid="result-divider"]:visible count:2')
  })

  it('releases a held gate and stops when the steer fails', async () => {
    const { script } = fakeScript(0, stepIndex => bodyRequest(stepIndex, steering))
    harness.steerFailure = new Error('The queued row offered no Steer.')
    await expect(exerciseSteerBeforeTool(context(script))).rejects.toBe(harness.steerFailure)
    expect(script.releaseGateIfHeld).toHaveBeenCalledTimes(1)
    expect(script.waitForSteps).not.toHaveBeenCalled()
  })

  it('fails when the request after the tool step lacks the steering message', async () => {
    const { script } = fakeScript(0, stepIndex => bodyRequest(stepIndex, 'The tool output alone.'))
    await expect(exerciseSteerBeforeTool(context(script))).rejects.toThrow('the steered request holds the inserted message')
  })
})

describe('exerciseSteerAfterTool', () => {
  const steering = 'Also append the word steered to your final reply.'

  it('lets the command write its second output after the steer, and requires both outputs in the next request', async () => {
    const { script } = fakeScript(0, stepIndex => bodyRequest(stepIndex, steering, 'NATIVEFIRSTMARKER', 'NATIVESECONDMARKER', 'yyyy-second-tail'))
    await exerciseSteerAfterTool(context(script), { expectDisplayedOutput: false })
    const outputs = harness.events.filter(event => /output|release|steer:/.test(event))
    expect(outputs).toEqual(['first output', `steer:${steering}`, 'release first', 'second output', 'release final', 'release first', 'release final'])
    expect(harness.events).toContain('request:1')
  })

  it('accepts a tool result whose middle the provider dropped, with the end of the second output kept', async () => {
    // Cline keeps the start and the end of a large output, so the second marker in the middle does not reach the model.
    const { script } = fakeScript(0, stepIndex => bodyRequest(stepIndex, steering, 'NATIVEFIRSTMARKER', '...[truncated 315 chars]...', 'yyyy-second-tail'))
    await expect(exerciseSteerAfterTool(context(script), { expectDisplayedOutput: false })).resolves.toBeUndefined()
  })

  it('fails when the next request lacks the second output', async () => {
    const { script } = fakeScript(0, stepIndex => bodyRequest(stepIndex, steering, 'NATIVEFIRSTMARKER', 'NATIVESECONDMARKER'))
    await expect(exerciseSteerAfterTool(context(script), { expectDisplayedOutput: false })).rejects.toThrow('the next request holds the end of the second output')
  })

  it('fails when the next request lacks the start of the first output', async () => {
    const { script } = fakeScript(0, stepIndex => bodyRequest(stepIndex, steering, 'yyyy-second-tail'))
    await expect(exerciseSteerAfterTool(context(script), { expectDisplayedOutput: false })).rejects.toThrow('the next request holds the start of the first output')
  })
})

describe('exerciseQueuedTurnWithoutSteering', () => {
  /** The first request holds the held prompt. The next holds the queued prompt and the first answer. */
  function turns(stepIndex: number, queued: readonly MockModelStep[]): MockModelRequestRecord {
    return stepIndex === 0
      ? bodyRequest(stepIndex, harness.sent[0] ?? '')
      : bodyRequest(stepIndex, harness.sent[0] ?? '', queued[0]?.text ?? '', harness.sent[1] ?? '')
  }

  /** Queue the prompt that the test sends while the gate holds the first turn, as the Worker does. */
  function queueSecondPrompt(script: ReturnType<typeof fakeScript>['script']) {
    script.waitForGate.mockImplementation(async () => {
      harness.events.push('gate held')
      harness.queueNextSend = { state: AgentInputState.QUEUED, canSteer: false, canPreempt: true }
    })
  }

  it('offers Preempt and no Steer, refuses the steer RPC, and runs the queued prompt as the next turn', async () => {
    const { script } = fakeScript(0, turns)
    queueSecondPrompt(script)
    await exerciseQueuedTurnWithoutSteering(context(script))
    expect(harness.events).toContain('queued row Steer count:0')
    expect(harness.events).toContain('queued row Preempt count:1')
    expect(harness.events).toContain('worker:SteerQueuedAgentInput')
    expect(harness.events.indexOf('worker:SteerQueuedAgentInput')).toBeLessThan(harness.events.indexOf('gate released'))
    // The page drops the queued row only after the held turn ends and the queued prompt runs.
    expect(harness.events.indexOf('queued row count:0')).toBeGreaterThan(harness.events.indexOf('gate released'))
    expect(harness.events.at(-1)).toBe('[data-testid="result-divider"]:visible count:2')
  })

  it('fails when the Worker offers no preemption of the held turn', async () => {
    harness.agent = { ...harness.agent, supportsPreemption: false }
    const { script } = fakeScript(0, turns)
    queueSecondPrompt(script)
    await expect(exerciseQueuedTurnWithoutSteering(context(script))).rejects.toThrow('the Worker offers preemption of the held turn')
    expect(harness.events).not.toContain('queued row Preempt count:1')
    expect(harness.events).not.toContain('gate released')
  })

  it('fails when the queued head cannot preempt the held turn', async () => {
    const end = startWaitLimitForTests(300)
    try {
      const { script } = fakeScript(0, turns)
      script.waitForGate.mockImplementation(async () => {
        harness.queueNextSend = { state: AgentInputState.QUEUED, canSteer: false, canPreempt: false }
      })
      await expect(exerciseQueuedTurnWithoutSteering(context(script))).rejects.toThrow('the queued prompt waits, offers no steer, and can preempt the held turn')
      expect(harness.events).not.toContain('worker:SteerQueuedAgentInput')
    }
    finally {
      end()
    }
  })

  it('fails when the held turn already read the queued prompt', async () => {
    const { script } = fakeScript(0, (stepIndex, queued) => bodyRequest(stepIndex, harness.sent[0] ?? '', harness.sent[1] ?? '', queued[0]?.text ?? ''))
    queueSecondPrompt(script)
    await expect(exerciseQueuedTurnWithoutSteering(context(script))).rejects.toThrow('NEXTQUEUEDPROMPT')
  })

  it('refuses a provider that steers before it sends anything', async () => {
    harness.agent = { ...harness.agent, supportsSteering: true }
    const { script } = fakeScript(0, turns)
    await expect(exerciseQueuedTurnWithoutSteering(context(script))).rejects.toThrow('the provider cannot steer a running turn')
    expect(harness.sent).toEqual([])
  })
})
