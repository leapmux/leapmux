import type { ManagedNativeScenarioContext } from './nativeScenario'
import { expectNoNativeControl } from './nativeControlObservation'

/** Observe an actual native control through its new applied idle edge. */
export async function expectNoNativeEditorRequest(
  context: ManagedNativeScenarioContext,
  options: { relatedProof: () => Promise<unknown> },
): Promise<void> {
  await expectNoNativeControl(context, { testId: 'dialog-editor', relatedProof: options.relatedProof })
}
