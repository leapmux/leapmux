import type { ControlResponseSummary } from '../../model/controlResponse'
import type { PersistedControlResponse } from '../../persistedControlResponse'
import { COPILOT_EVENT } from '~/generated/contracts/copilot-protocol'
import { MCP_ELICITATION_ACTION } from '~/generated/contracts/mcp-elicitation'
import { pickObject, pickString } from '~/lib/jsonPick'
import { permissionOptionLabel } from '../../controls/permissionOptionLabels'
import { CONTROL_DECISION_WORDS, feedback, feedbackOrLabel, label } from '../../persistedControlResponse'
import { copilotDecisionOption } from './permissionOptions'
import { copilotEvent } from './protocol'

/** The runtime withdraws a request nobody answered, which is a state and not a decision. */
const COPILOT_DECISION_CANCELLED = 'cancelled'

/**
 * The display for one saved Copilot answer.
 *
 * The stored row keeps the NATIVE response bytes, so this reads the runtime's own
 * decision word rather than a label LeapMux chose at answer time. That is what keeps
 * a reloaded transcript honest about what the runtime received.
 *
 * A PERMISSION decision then reads back through the option list its own buttons drew,
 * so the saved row carries the words the reader clicked. Every Agent Client Protocol
 * provider resolves a saved decision the same way, through the same helper.
 */
export function copilotControlResponseSummary(cr: PersistedControlResponse): ControlResponseSummary | null {
  const answer = pickObject(pickObject(cr.response, 'response'), 'response')
  if (!answer)
    return null
  switch (copilotEvent(cr.request)?.type) {
    case COPILOT_EVENT.PermissionRequested: {
      const decision = pickString(answer, 'kind')
      if (decision === COPILOT_DECISION_CANCELLED)
        return label('Cancelled')
      const option = copilotDecisionOption(cr.request, decision)
      if (!option)
        return null
      const words = permissionOptionLabel(option)
      // A rejection may carry the reader's own reason, which says more than the word.
      return option.kind.startsWith('reject')
        ? feedbackOrLabel(pickString(answer, 'feedback'), words)
        : label(words)
    }
    case COPILOT_EVENT.UserInputRequested: {
      // An empty answer is a real answer the runtime accepts, so it reads as one
      // rather than as a missing decision.
      const text = pickString(answer, 'answer')
      return label(text || 'Answered with an empty response')
    }
    // A plan approval and an elicitation state no option list, so their words come from
    // the buttons those controls draw: Approve and Reject.
    case COPILOT_EVENT.ExitPlanModeRequested: {
      if (answer.approved === true)
        return label(CONTROL_DECISION_WORDS.plan.allow)
      const reason = pickString(answer, 'feedback')
      return reason ? feedback(reason) : label(CONTROL_DECISION_WORDS.plan.deny)
    }
    case COPILOT_EVENT.ElicitationRequested:
      switch (pickString(answer, 'action')) {
        case MCP_ELICITATION_ACTION.Accept:
          return label(CONTROL_DECISION_WORDS.plan.allow)
        case MCP_ELICITATION_ACTION.Decline:
          return label(CONTROL_DECISION_WORDS.plan.deny)
        case MCP_ELICITATION_ACTION.Cancel:
          // The third button, which neither approves nor rejects.
          return label('Cancel')
        default:
          return null
      }
    default:
      return null
  }
}
