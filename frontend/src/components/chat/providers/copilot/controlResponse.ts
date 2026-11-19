import type { PermissionOption } from '../../model/controlPrompt'
import type { ControlResponseSummary } from '../../model/controlResponse'
import type { PersistedControlResponse } from '../../persistedControlResponse'
import { COPILOT_APPROVAL_SCOPE, COPILOT_AUTO_MODE_SWITCH_RESPONSE, COPILOT_DECISION, COPILOT_SAMPLING_DECISION } from '~/generated/contracts/copilot-protocol'
import { MCP_ELICITATION_ACTION } from '~/generated/contracts/mcp-elicitation'
import { pickObject, pickString } from '~/lib/jsonPick'
import { permissionOptionLabel } from '../../controls/permissionOptionLabels'
import { isRejectPermissionKind } from '../../model/controlPrompt'
import { CONTROL_DECISION_WORDS, feedback, feedbackOrLabel, label } from '../../persistedControlResponse'

/** The runtime withdraws a request nobody answered, which is a state and not a decision. */
const COPILOT_DECISION_CANCELLED = 'cancelled'

/**
 * The option id whose button sends one native approval word.
 *
 * The answer LeapMux sends carries a SCOPE (`{behavior:'allow', scope:'session'}`),
 * and the worker turns that into the runtime's own word before it stores the answer
 * (`copilotNativeAnswer` in the Go `copilot` package). A saved row therefore holds the
 * word, and this reads it back to the option of the recorded prompt. The refusal is
 * absent on purpose: the prompt offers exactly one option of a reject kind, so
 * `copilotChosenOption` finds it by its kind.
 */
const DECISION_OPTION_IDS: Record<string, string> = {
  [COPILOT_DECISION.ApproveOnce]: COPILOT_APPROVAL_SCOPE.Once,
  [COPILOT_DECISION.ApproveForSession]: COPILOT_APPROVAL_SCOPE.Session,
  [COPILOT_DECISION.ApproveForLocation]: COPILOT_APPROVAL_SCOPE.Project,
}

/**
 * The option of the recorded prompt that a saved decision word chose, or undefined
 * for a word this build does not know or an option the prompt does not offer.
 *
 * `Object.hasOwn`, never a bare index read: the decision word is wire data off a
 * PERSISTED row, so `toString`, `constructor`, `valueOf` and `__proto__` all reach
 * here, and an index read answers each with a FUNCTION from `Object.prototype`. A
 * function passes a `??` fallback, and the caller then reads it as an option id.
 */
function copilotChosenOption(decision: string, options: readonly PermissionOption[]): PermissionOption | undefined {
  if (decision === COPILOT_DECISION.Reject)
    return options.find(option => isRejectPermissionKind(option.kind))
  if (!Object.hasOwn(DECISION_OPTION_IDS, decision))
    return undefined
  const optionId = DECISION_OPTION_IDS[decision]
  return options.find(option => option.optionId === optionId)
}

/**
 * The display of one saved answer to a request that a permission prompt states.
 *
 * Three native requests share that prompt kind, and the shape of the saved answer tells
 * them apart:
 *
 *   - The automatic model switch states its answer as a bare word.
 *   - An MCP server's sampling request states a `decision`.
 *   - A tool's permission request states the runtime's decision word as `kind`.
 *
 * A tool's permission reads back through the option list of the recorded prompt, so
 * the saved row states the selected option, such as "Allow once": the words of the
 * button the reader pressed.
 */
function copilotPermissionSummary(envelope: Record<string, unknown> | null, options: readonly PermissionOption[]): ControlResponseSummary | null {
  switch (pickString(envelope, 'response')) {
    case COPILOT_AUTO_MODE_SWITCH_RESPONSE.Yes:
      return label('Switched to Auto for this turn')
    case COPILOT_AUTO_MODE_SWITCH_RESPONSE.YesAlways:
      return label('Switched to Auto from now on')
    case COPILOT_AUTO_MODE_SWITCH_RESPONSE.No:
      return label('Declined the switch to Auto')
  }
  const answer = pickObject(envelope, 'response')
  if (!answer)
    return null
  switch (pickString(answer, 'decision')) {
    case COPILOT_SAMPLING_DECISION.Allow:
      return label(CONTROL_DECISION_WORDS.permission.allow)
    case COPILOT_SAMPLING_DECISION.Deny:
      return label(CONTROL_DECISION_WORDS.permission.deny)
  }
  const decision = pickString(answer, 'kind')
  if (decision === COPILOT_DECISION_CANCELLED)
    return label('Cancelled')
  const option = copilotChosenOption(decision, options)
  if (!option)
    return null
  const words = permissionOptionLabel(option)
  // A rejection may carry the reader's own reason, which says more than the word.
  return isRejectPermissionKind(option.kind)
    ? feedbackOrLabel(pickString(answer, 'feedback'), words)
    : label(words)
}

/**
 * The display for one saved Copilot answer.
 *
 * The stored row keeps the NATIVE response bytes, so this reads the runtime's own
 * decision word rather than a label LeapMux chose at answer time. That is what keeps
 * a reloaded transcript honest about what the runtime received.
 *
 * The prompt that the Worker recorded with the answer states what the request asked,
 * and the words its buttons carried. A row that records no prompt reads as nothing,
 * so the caller falls back to the shared label.
 */
export function copilotControlResponseSummary(cr: PersistedControlResponse): ControlResponseSummary | null {
  const envelope = pickObject(cr.response, 'response')
  switch (cr.prompt?.kind) {
    case 'permission':
      return copilotPermissionSummary(envelope, cr.prompt.permission.options)
    case 'question': {
      const answer = pickObject(envelope, 'response')
      if (!answer)
        return null
      // An empty answer is a real answer the runtime accepts, so it reads as one
      // rather than as a missing decision.
      const text = pickString(answer, 'answer')
      return label(text || 'Answered with an empty response')
    }
    // A plan approval and an elicitation state no option list, so their words come from
    // the buttons those controls draw: Approve and Reject.
    case 'plan': {
      const answer = pickObject(envelope, 'response')
      if (!answer)
        return null
      if (answer.approved === true)
        return label(CONTROL_DECISION_WORDS.plan.allow)
      const reason = pickString(answer, 'feedback')
      return reason
        ? feedback(reason)
        : label(CONTROL_DECISION_WORDS.plan.deny)
    }
    case 'elicitation':
      switch (pickString(pickObject(envelope, 'response'), 'action')) {
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
