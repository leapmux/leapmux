import type { ProviderControlCapability } from '../capabilities'
import { AMP_PERMISSION_MODE } from '~/generated/contracts/amp-protocol'
import { buildDenyResponse } from '~/utils/controlResponse'
import { controlBehaviorDisplay } from '../../persistedControlResponse'
import { ampExtractControl } from './extractControl'

/** The complete Amp control channel, separate from provider registration. */
export const ampControls: ProviderControlCapability = {
  extractControl: ampExtractControl,
  // The worker answers the helper from the neutral envelope, and the saved row holds
  // that envelope, so the neutral reading is the whole display.
  controlResponseDisplay: cr => controlBehaviorDisplay(cr.response),
  // The composer's send is a refusal, and its text rides the refusal as the reason
  // Amp hands the model. Allow lives on its own button.
  buildControlResponse: (_payload, content, requestId) => buildDenyResponse(requestId, content),
  // Amp has two modes. Allow All answers every call at once, which is what Bypass means.
  // No mode asks for the risky calls alone, so Amp offers no Smart preset.
  permissionPresets: { bypass: { sets: { permissionMode: AMP_PERMISSION_MODE.AllowAll } } },
}
