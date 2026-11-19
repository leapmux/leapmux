import type { ControlResponseDeriver } from '../persistedControlResponse'
import { WORKSPACE_TRUST_CONTROL_TYPE, WORKSPACE_TRUST_DECISION, WORKSPACE_TRUST_FIELD } from '~/generated/contracts/workspace-trust'
import { pickObject, pickString } from '~/lib/jsonPick'
import { label } from '../persistedControlResponse'

/**
 * The answer to the workspace-trust question that LeapMux asks for an agent.
 *
 * Some agents load part of a project's configuration only from a folder they trust,
 * but ask nothing in the mode that LeapMux runs them in. The Worker then asks
 * (`providerkit.WorkspaceTrust`), reads the question into a permission prompt, and
 * saves the answer as `trust` or `reject` (`contracts/workspace-trust.json`). This
 * module reads that saved answer for every plugin whose agent the Worker asks for.
 *
 * The question is LeapMux's own shape, so it is neutral.
 */

/** Whether one stored request payload is the workspace-trust question. */
export function isWorkspaceTrustQuestion(payload: Record<string, unknown> | undefined): boolean {
  return pickString(payload, WORKSPACE_TRUST_FIELD.Type) === WORKSPACE_TRUST_CONTROL_TYPE.Question
}

/** Read a saved answer to the workspace-trust question, and hand every other answer to `display`. */
export function withWorkspaceTrustResponse(display: ControlResponseDeriver): ControlResponseDeriver {
  return (cr) => {
    if (!isWorkspaceTrustQuestion(cr.request))
      return display(cr)
    const answer = pickObject(pickObject(cr.response, 'response'), 'response')
    switch (pickString(answer, WORKSPACE_TRUST_FIELD.Decision)) {
      case WORKSPACE_TRUST_DECISION.Trust:
        return label('Trusted this folder')
      case WORKSPACE_TRUST_DECISION.Reject:
        return label('Did not trust this folder')
      default:
        return null
    }
  }
}
