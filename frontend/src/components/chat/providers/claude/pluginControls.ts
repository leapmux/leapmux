import type { ProviderControlCapability } from '../capabilities'
import { controlBehaviorDisplay, controlDecisionWords } from '../../persistedControlResponse'
import { claudePermissionPresets } from './permissionPresets'

/** The Claude control facts that the browser states, separate from provider registration. */
export const claudeControls: ProviderControlCapability = {
  permissionPresets: claudePermissionPresets,
  // Claude's saved answer is the neutral behavior envelope, so the shared reader derives its display.
  // The prompt that the Worker recorded selects the plan or the permission words that the saved row shows.
  controlResponseDisplay: cr => controlBehaviorDisplay(cr.response, controlDecisionWords(cr.prompt)),
}
