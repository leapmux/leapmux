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

// `allowNativeOperation` has its own cases in `./nativePermission.test.ts`. This double records what the proof hands it,
// shows the banner to the check of the proof, and runs the extra proof after the decision.
vi.mock('./nativePermission', () => ({
  createNativePermissionFileWrite: async (_context: unknown, options: Record<string, unknown>) => {
    harness.events.push(`default write:${JSON.stringify(options)}`)
    if (!harness.defaultPlan)
      throw new Error('The test gives no default plan.')
    return harness.defaultPlan
  },
  allowNativeOperation: (_context: unknown, operation: NativePermissionOperationPlan, nativeProof?: (request: MockModelRequestRecord) => void | Promise<void>) =>
    async (checkBanner: (banner: Locator) => Promise<void>) => {
      harness.decisions.push({ operation, nativeProof })
      harness.events.push('decision banner')
      await checkBanner(BANNER)
      harness.events.push('decision allow')
      await nativeProof?.(RESULT_REQUEST)
      return RESULT_REQUEST
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
    providerAgent: { provider: AgentProvider.KIMI_CODE, prefix: 'native-e2e' },
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
  it('writes the default native file and allows it while the observation checks the banner', async () => {
    harness.defaultPlan = plan('default')
    await exerciseUnsupportedControlThroughPermission(context(), { purpose: 'editor', classify: permissionClassifier() })
    expect(harness.events).toEqual([
      `default write:${JSON.stringify(defaultControlPermissionWrite('editor'))}`,
      'observe:dialog-editor',
      'decision banner',
      'banner visible',
      'no editor:0',
      'decision allow',
      'no editor:0',
      'watch cancelled',
    ])
    expect(harness.decisions).toEqual([{ operation: harness.defaultPlan, nativeProof: undefined }])
  })

  it('allows the caller operation and hands it the extra proof', async () => {
    const gate = { shown: async () => {} } as unknown as GatedOutput
    const operation = plan('caller', gate)
    const nativeProof = (request: MockModelRequestRecord) => {
      harness.events.push(`extra proof:${request.stepIndex}`)
    }
    await exerciseUnsupportedControlThroughPermission(context(), {
      purpose: 'workspace-trust',
      classify: permissionClassifier(),
      operation,
      nativeProof,
    })
    expect(harness.events.some(event => event.startsWith('default write'))).toBe(false)
    expect(harness.decisions).toEqual([{ operation, nativeProof }])
    expect(harness.events).toContain('extra proof:1')
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

  it('cancels the watch when the allowed operation fails', async () => {
    harness.defaultPlan = plan('default')
    const failure = new Error('The native result is absent.')
    await expect(exerciseUnsupportedControlThroughPermission(context(), {
      purpose: 'editor',
      classify: permissionClassifier(),
      nativeProof: () => {
        throw failure
      },
    })).rejects.toBe(failure)
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
