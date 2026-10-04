import type { Locator } from '@playwright/test'
import type { ControlExtractionInput, ExtractedControlRequest } from '../../../src/components/chat/providers/capabilities'
import type { ManagedNativeScenarioContext } from './nativeScenario'
import { expect } from '@playwright/test'
import { withCleanup } from './cleanup'
import { expectNoNativeControl } from './nativeControlObservation'
import { watchNativeControls } from './nativeControlWatch'
import { currentNativeAgent } from './nativeScenario'

/** Check a source-proved missing route against real provider controls and the browser classifier. */
export async function exerciseUnsupportedNativeControl(
  context: ManagedNativeScenarioContext,
  options: {
    purpose: 'editor' | 'workspace-trust' | 'question'
    isQuestionRequest?: (payload: Record<string, unknown>) => boolean
    classify: (input: ControlExtractionInput) => ExtractedControlRequest | null
    nativeOperation: (beforeDecision: (banner: Locator) => Promise<void>) => Promise<void>
  },
): Promise<void> {
  const agent = await currentNativeAgent(context)
  const watch = await watchNativeControls(context.leapmuxServer, agent.id)
  const nativeProof = async () => {
    await options.nativeOperation(async (banner) => {
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
        expect(options.isQuestionRequest(frame.payload)).toBe(false)
      if (control?.kind === 'permission' && options.purpose === 'workspace-trust')
        expect(control.permission.title).not.toMatch(/Trust the workspace|Do not trust/)
    }
    await expect(context.page.locator('[data-testid="dialog-editor"]:visible')).toHaveCount(0)
  }
  const surfaces = options.purpose === 'question' ? ['control-question-group', 'elicitation-form'] : ['dialog-editor']
  let observe = nativeProof
  for (const testId of surfaces) {
    const relatedControl = observe
    observe = () => expectNoNativeControl(context, { testId, relatedControl })
  }
  await withCleanup(observe, async () => watch.cancel())
}
