import type { Locator, Page } from '@playwright/test'
import type { MockModelRequestRecord } from './mockModelScript'
import type { ModelScript } from './modelScriptFixture'
import type { NativePermissionOperationPlan } from './nativePermission'
import type { ManagedNativeScenarioContext } from './nativeScenario'
import type { GatedOutput } from './outputGate'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { defaultControlPermissionWrite, exerciseUnsupportedControlThroughPermission } from './unsupportedNativeControl'

/** The steps of the proof, in order, and the doubles of its collaborators. */
const harness = vi.hoisted(() => ({
  events: [] as string[],
  frames: [] as { payload: Record<string, unknown> }[],
  decisions: [] as Record<string, unknown>[],
  defaultPlan: undefined as NativePermissionOperationPlan | undefined,
}))

/** The request that the allowed operation produces. */
const RESULT_REQUEST: MockModelRequestRecord = { protocol: 'openai-chat-completions', path: '/v1/chat/completions', stepIndex: 1, body: {} }

/** A fake locator that the mocked `expect` recognizes. */
const BANNER = { fake: 'banner' } as unknown as Locator

vi.mock('./nativeScenario', () => ({
  currentNativeAgent: async () => ({ id: 'native-agent' }),
}))

vi.mock('./nativeControlWatch', () => ({
  watchNativeControls: async () => ({
    controls: () => harness.frames,
    cancel: () => {
      harness.events.push('watch cancelled')
    },
  }),
}))

vi.mock('./nativeControlObservation', () => ({
  expectNoNativeControl: async (_context: unknown, options: { testId: string, relatedProof: () => Promise<void> }) => {
    harness.events.push(`observe:${options.testId}`)
    await options.relatedProof()
  },
}))

vi.mock('./nativePermission', () => ({
  createNativePermissionFileWrite: async (_context: unknown, options: Record<string, unknown>) => {
    harness.events.push(`default write:${JSON.stringify(options)}`)
    if (!harness.defaultPlan)
      throw new Error('The test gives no default plan.')
    return harness.defaultPlan
  },
  exerciseNativePermissionDecision: async (_context: unknown, options: Record<string, unknown> & {
    beforeDecision: (banner: Locator) => Promise<void>
    nativeProof: (request: MockModelRequestRecord) => Promise<void>
  }) => {
    harness.decisions.push(options)
    harness.events.push('decision banner')
    await options.beforeDecision(BANNER)
    harness.events.push(`decision ${String(options.decision)}`)
    await options.nativeProof(RESULT_REQUEST)
  },
}))

vi.mock('@playwright/test', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@playwright/test')>()
  const fakeExpect = (value: unknown, message?: string) => {
    if (typeof value === 'object' && value !== null && 'fake' in value) {
      return {
        toBeVisible: async () => {
          harness.events.push('banner visible')
        },
        toHaveCount: async (count: number) => {
          harness.events.push(`no editor:${count}`)
        },
        not: { toContainText: async () => {} },
      }
    }
    return expect(value, message)
  }
  return { ...actual, expect: Object.assign(fakeExpect, { poll: (read: () => unknown, options?: { message?: string }) => expect.poll(read, options) }) }
})

function context(): ManagedNativeScenarioContext {
  return {
    page: { locator: () => ({ fake: 'editor' }) } as unknown as Page,
    modelScript: {} as ModelScript,
    provider: AgentProvider.KIMI_CODE,
    workspaceId: 'unsupported-control-workspace',
    leapmuxServer: { hubUrl: 'http://unused.invalid', adminToken: 'unused-token', workerId: 'unused-worker' },
  }
}

/** A plan that records its own guard and proof. */
function plan(name: string, outputGate?: GatedOutput): NativePermissionOperationPlan {
  return {
    toolCall: { id: `${name}-call`, name: 'bash', arguments: { command: name } },
    ...(outputGate ? { outputGate } : {}),
    beforeDecision: () => {
      harness.events.push(`${name} guard`)
    },
    nativeProof: (request) => {
      harness.events.push(`${name} proof:${request.stepIndex}`)
    },
  }
}

/** Classify every frame as one permission. */
function permissionClassifier() {
  return () => ({ kind: 'permission' as const, permission: { title: 'Run the command', options: [] } })
}

beforeEach(() => {
  harness.events.length = 0
  harness.decisions.length = 0
  harness.frames = [{ payload: { method: 'permission' } }]
  harness.defaultPlan = undefined
})

describe('exerciseUnsupportedControlThroughPermission', () => {
  it('writes the default native file and runs its guard before the control guard', async () => {
    harness.defaultPlan = plan('default')
    await exerciseUnsupportedControlThroughPermission(context(), { purpose: 'editor', classify: permissionClassifier() })
    expect(harness.events).toEqual([
      `default write:${JSON.stringify(defaultControlPermissionWrite('editor'))}`,
      'observe:dialog-editor',
      'decision banner',
      'default guard',
      'banner visible',
      'no editor:0',
      'decision allow',
      'default proof:1',
      'no editor:0',
      'watch cancelled',
    ])
    expect(harness.decisions[0]).toMatchObject({ toolCall: harness.defaultPlan.toolCall, decision: 'allow' })
    expect(harness.decisions[0]).not.toHaveProperty('outputGate')
  })

  it('uses the caller operation, forwards its output gate, and runs the extra proof after the operation proof', async () => {
    const gate = { shown: async () => {} } as unknown as GatedOutput
    const operation = plan('caller', gate)
    await exerciseUnsupportedControlThroughPermission(context(), {
      purpose: 'workspace-trust',
      classify: permissionClassifier(),
      operation,
      nativeProof: (request) => {
        harness.events.push(`extra proof:${request.stepIndex}`)
      },
    })
    expect(harness.events.some(event => event.startsWith('default write'))).toBe(false)
    expect(harness.decisions[0]).toMatchObject({ toolCall: operation.toolCall, outputGate: gate })
    const proofs = harness.events.filter(event => event.includes('proof'))
    expect(proofs).toEqual(['caller proof:1', 'extra proof:1'])
    expect(harness.events.indexOf('caller guard')).toBeLessThan(harness.events.indexOf('banner visible'))
  })

  it('observes both question surfaces and refuses a frame that the provider marks as a question', async () => {
    harness.defaultPlan = plan('default')
    await expect(exerciseUnsupportedControlThroughPermission(context(), {
      purpose: 'question',
      classify: permissionClassifier(),
      isQuestionRequest: payload => payload.method === 'permission',
    })).rejects.toThrow('the provider sends no native question request')
    expect(harness.events.slice(1, 3)).toEqual(['observe:elicitation-form', 'observe:control-question-group'])
    expect(harness.events.at(-1)).toBe('watch cancelled')
  })

  it('stops before the decision when the operation guard fails', async () => {
    const failure = new Error('The target changed before the decision.')
    harness.defaultPlan = { ...plan('default'), beforeDecision: () => {
      throw failure
    } }
    await expect(exerciseUnsupportedControlThroughPermission(context(), { purpose: 'editor', classify: permissionClassifier() })).rejects.toBe(failure)
    expect(harness.events).not.toContain('banner visible')
    expect(harness.events).not.toContain('decision allow')
    expect(harness.events.at(-1)).toBe('watch cancelled')
  })
})

describe('defaultControlPermissionWrite', () => {
  it.each(['editor', 'workspace-trust', 'question'] as const)('gives the %s proof its own file and call ID', (purpose) => {
    expect(defaultControlPermissionWrite(purpose)).toEqual({
      fileName: `native-${purpose}-control.txt`,
      callId: `native-${purpose}-permission`,
      outputPrefix: 'NATIVECONTROL',
    })
  })
})
