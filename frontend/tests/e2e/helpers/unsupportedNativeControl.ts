import type { Locator } from '@playwright/test'
import type { ControlExtractionInput, ExtractedControlRequest } from '../../../src/components/chat/providers/capabilities'
import type { MockModelRequestRecord } from './mockModelScript'
import type { NativePermissionOperationPlan } from './nativePermission'
import type { ManagedNativeScenarioContext } from './nativeScenario'
import { expect } from '@playwright/test'
import { withCleanup } from './cleanup'
import { expectNoNativeControl } from './nativeControlObservation'
import { watchNativeControls } from './nativeControlWatch'
import { createNativePermissionFileWrite, exerciseNativePermissionDecision } from './nativePermission'
import { currentNativeAgent } from './nativeScenario'

/** The control route that a provider lacks. */
export type UnsupportedControlPurpose = 'editor' | 'workspace-trust' | 'question'

/** Read one native control payload into the browser's control model. */
export type NativeControlClassifier = (input: ControlExtractionInput) => ExtractedControlRequest | null

/** Check a source-proved missing route against real provider controls and the browser classifier. */
export async function exerciseUnsupportedNativeControl(
  context: ManagedNativeScenarioContext,
  options: {
    purpose: UnsupportedControlPurpose
    isQuestionRequest?: (payload: Record<string, unknown>) => boolean
    classify: NativeControlClassifier
    /** Run the related native operation. Its result, such as the model request that a decision helper returns, is unused. */
    relatedProof: (beforeDecision: (banner: Locator) => Promise<void>) => Promise<unknown>
  },
): Promise<void> {
  const agent = await currentNativeAgent(context)
  const watch = await watchNativeControls(context.leapmuxServer, agent.id)
  const nativeProof = async () => {
    await options.relatedProof(async (banner) => {
      await expect.poll(() => watch.controls().some(frame => options.classify({ payload: frame.payload })?.kind === 'permission')).toBe(true)
      const permissions = watch.controls().map(frame => options.classify({ payload: frame.payload })).filter(value => value?.kind === 'permission')
      expect(permissions.length).toBeGreaterThan(0)
      await expect(banner).toBeVisible()
      await expect(context.page.locator('[data-testid="dialog-editor"]:visible')).toHaveCount(0)
      if (options.purpose === 'workspace-trust')
        await expect(banner).not.toContainText(/Trust the workspace|Do not trust/)
    })
    const frames = watch.controls()
    expect(frames.length).toBeGreaterThan(0)
    for (const frame of frames) {
      const control = options.classify({ payload: frame.payload })
      expect(control?.kind).not.toBe('dialog')
      if (options.purpose === 'question' && options.isQuestionRequest)
        expect(options.isQuestionRequest(frame.payload), 'the provider sends no native question request').toBe(false)
      if (control?.kind === 'permission' && options.purpose === 'workspace-trust')
        expect(control.permission.title).not.toMatch(/Trust the workspace|Do not trust/)
    }
    await expect(context.page.locator('[data-testid="dialog-editor"]:visible')).toHaveCount(0)
  }
  const surfaces = options.purpose === 'question' ? ['control-question-group', 'elicitation-form'] : ['dialog-editor']
  let observe = nativeProof
  for (const testId of surfaces) {
    const inner = observe
    observe = () => expectNoNativeControl(context, { testId, relatedProof: inner })
  }
  await withCleanup(observe, async () => watch.cancel())
}

/** How {@link exerciseUnsupportedControlThroughPermission} proves a missing control route. */
export interface UnsupportedControlThroughPermissionOptions {
  purpose: UnsupportedControlPurpose
  classify: NativeControlClassifier
  isQuestionRequest?: (payload: Record<string, unknown>) => boolean
  /**
   * The real native operation whose permission the reader allows. By default, a native file write in the agent's
   * working directory (see `createNativePermissionFileWrite`). A provider that asks only for another operation
   * passes its own.
   */
  operation?: NativePermissionOperationPlan
  /** A proof of the request after the allowed operation, in addition to the proof of the operation itself. */
  nativeProof?: (request: MockModelRequestRecord) => void | Promise<void>
}

/**
 * The native file write that proves a working permission, for one purpose.
 * The names are fixed, so one agent runs this proof once: a second write fails its guard, because the file exists.
 */
export function defaultControlPermissionWrite(purpose: UnsupportedControlPurpose): { fileName: string, callId: string, outputPrefix: string } {
  return { fileName: `native-${purpose}-control.txt`, callId: `native-${purpose}-permission`, outputPrefix: 'NATIVECONTROL' }
}

/**
 * Prove that a provider raises no control of the missing route, while a real native permission still works.
 *
 * The reader allows one real native operation through its permission banner, and the proof observes every control
 * that the provider sends during it (see `exerciseUnsupportedNativeControl`). The operation's own guard runs first,
 * before the decision, so it reads the target before anything can change it. The observation of the control frames
 * runs after it. An output gate of the operation goes with its tool call.
 */
export async function exerciseUnsupportedControlThroughPermission(
  context: ManagedNativeScenarioContext,
  options: UnsupportedControlThroughPermissionOptions,
): Promise<void> {
  const operation = options.operation ?? await createNativePermissionFileWrite(context, defaultControlPermissionWrite(options.purpose))
  await exerciseUnsupportedNativeControl(context, {
    purpose: options.purpose,
    classify: options.classify,
    ...(options.isQuestionRequest ? { isQuestionRequest: options.isQuestionRequest } : {}),
    relatedProof: beforeDecision => exerciseNativePermissionDecision(context, {
      toolCall: operation.toolCall,
      ...(operation.outputGate ? { outputGate: operation.outputGate } : {}),
      decision: 'allow',
      beforeDecision: async (banner) => {
        await operation.beforeDecision()
        await beforeDecision(banner)
      },
      nativeProof: async (request) => {
        await operation.nativeProof(request)
        await options.nativeProof?.(request)
      },
    }),
  })
}
