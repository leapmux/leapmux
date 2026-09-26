import type { ProviderControlCapability } from '../capabilities'
import { buildAllowResponse, buildDenyResponse, getToolInput } from '~/utils/controlResponse'
import { codebuddyExtractControl } from './extractControl'

/**
 * The CodeBuddy control channel.
 *
 * The answer the worker sends is CodeBuddy's own `{"allowed":true}`, so the
 * shared Allow/Deny envelope is what the browser sends and the worker
 * translates. The options stay empty for the same reason: the shared pair is
 * the surface the reader answers.
 */
export const codebuddyControls: ProviderControlCapability = {
  buildControlResponse(payload, content, requestId) {
    // An editor reply to a plan always rejects it with feedback. The dedicated
    // approval button owns the allow path.
    if (codebuddyExtractControl({ payload })?.kind === 'plan')
      return buildDenyResponse(requestId, content)
    return content
      ? buildDenyResponse(requestId, content)
      : buildAllowResponse(requestId, getToolInput(payload))
  },
  extractControl: codebuddyExtractControl,
}
