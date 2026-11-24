import type { Locator } from '@playwright/test'
import type { MockModelRequestRecord } from './mockModelScript'
import type { NativePermissionOperationPlan } from './nativePermission'
import type { ManagedNativeScenarioContext } from './nativeScenario'
import { expect } from '@playwright/test'
import { withCleanup } from './cleanup'
import { expectNoNativeControl } from './nativeControlObservation'
import { readsProviderPermission, watchNativeControls } from './nativeControlWatch'
import { allowNativeOperation, createNativePermissionFileWrite } from './nativePermission'
import { currentNativeAgent } from './nativeScenario'

/** The control route that a provider lacks. */
export type UnsupportedControlPurpose = 'editor' | 'workspace-trust' | 'question'

/** Check a source-proved missing route against real provider controls and the prompts that the Worker reads from them. */
export async function exerciseUnsupportedNativeControl(
  context: ManagedNativeScenarioContext,
  options: {
    purpose: UnsupportedControlPurpose
    /** The spec's own test of a native question payload, beside the prompt that the Worker reads from it. */
    isQuestionRequest?: (payload: Record<string, unknown>) => boolean
    /** Run the related native operation. Its result, such as the model request that a decision helper returns, is unused. */
    relatedProof: (beforeDecision: (banner: Locator) => Promise<void>) => Promise<unknown>
  },
): Promise<void> {
  const agent = await currentNativeAgent(context)
  const watch = await watchNativeControls(context.leapmuxServer, agent.id)
  const nativeProof = async () => {
    await options.relatedProof(async (banner) => {
      await expect.poll(() => watch.controls().some(readsProviderPermission)).toBe(true)
      await expect(banner).toBeVisible()
      await expect(context.page.locator('[data-testid="dialog-editor"]:visible')).toHaveCount(0)
      if (options.purpose === 'workspace-trust')
        await expect(banner).not.toContainText(/Trust the workspace|Do not trust/)
    })
    const frames = watch.controls()
    expect(frames.length).toBeGreaterThan(0)
    for (const frame of frames) {
      const control = frame.prompt
      expect(control?.kind).not.toBe('dialog')
      if (options.purpose === 'question') {
        expect(control?.kind, 'the provider sends no native question request').not.toBe('question')
        if (options.isQuestionRequest)
          expect(options.isQuestionRequest(frame.payload), 'the provider sends no native question request').toBe(false)
      }
      if (control?.kind === 'permission' && options.purpose === 'workspace-trust')
        expect(control.permission.title ?? '').not.toMatch(/Trust the workspace|Do not trust/)
    }
    await expect(context.page.locator('[data-testid="dialog-editor"]:visible')).toHaveCount(0)
  }
  const surfaces = options.purpose === 'question'
    ? ['control-question-group', 'elicitation-form']
    : ['dialog-editor']
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
  /** The spec's own test of a native question payload, beside the prompt that the Worker reads from it. */
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
    ...(options.isQuestionRequest
      ? { isQuestionRequest: options.isQuestionRequest }
      : {}),
    relatedProof: allowNativeOperation(context, operation, options.nativeProof),
  })
}
