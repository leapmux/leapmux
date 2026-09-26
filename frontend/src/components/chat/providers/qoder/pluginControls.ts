import type { ProviderControlCapability } from '../capabilities'
import { buildAllowResponse, buildDenyResponse, getToolInput } from '~/utils/controlResponse'
import { qoderExtractControl } from './extractControl'

/**
 * The Qoder control channel.
 *
 * The browser sends the neutral behavior envelope and the worker translates it
 * into Qoder's `{behavior, outcome}` answer, so the shared Allow/Deny pair is
 * the surface.
 */
export const qoderControls: ProviderControlCapability = {
  buildControlResponse(payload, content, requestId) {
    // An editor reply to a plan always rejects it with feedback. The dedicated
    // approval button owns the allow path.
    if (qoderExtractControl({ payload })?.kind === 'plan')
      return buildDenyResponse(requestId, content)
    return content
      ? buildDenyResponse(requestId, content)
      : buildAllowResponse(requestId, getToolInput(payload))
  },
  extractControl: qoderExtractControl,
}
