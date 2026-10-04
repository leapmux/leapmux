import type { ControlResponseSummary } from '../../model/controlResponse'
import type { PersistedControlResponse } from '../../persistedControlResponse'
import { CLINE_APPROVAL_REPLY, CLINE_CAPABILITY_REPLY, CLINE_DECLINE_REASON, CLINE_EVENT, CLINE_PERMISSION_MODE, CLINE_TOOL } from '~/generated/contracts/cline-protocol'
import { isObject, pickObject, pickString } from '~/lib/jsonPick'
import { CONTROL_DECISION_WORDS, feedbackOrLabel, label } from '../../persistedControlResponse'
import { clinePayload } from './protocol'

/** The words of each mode an approved plan can switch to. */
const PLAN_MODE_WORDS: ReadonlyMap<string, string> = new Map([
  [CLINE_PERMISSION_MODE.Act, 'Act'],
  [CLINE_PERMISSION_MODE.AutoApprove, 'Auto-approve'],
])

/**
 * The display of a saved Cline answer.
 *
 * The saved row holds the answer the worker sent, in Cline's own shape: an approval
 * reply `{approvalId, approved, reason?, permissionMode?}`, or a capability reply
 * `{requestId, ok, payload: {result}}` for a question and `{requestId, ok: false,
 * error}` for a declined one. The stored request is Cline's own event, which states
 * which of the two it answers.
 */
export function clineControlResponseSummary(cr: PersistedControlResponse): ControlResponseSummary | null {
  const reply = cr.response
  if (!isObject(reply))
    return null
  const approval = clinePayload(cr.request, CLINE_EVENT.ApprovalRequested)
  if (approval)
    return approvalSummary(pickString(approval, 'toolName'), reply)
  if (clinePayload(cr.request, CLINE_EVENT.CapabilityRequested))
    return questionSummary(reply)
  return null
}

function approvalSummary(toolName: string, reply: Record<string, unknown>): ControlResponseSummary | null {
  if (typeof reply[CLINE_APPROVAL_REPLY.Approved] !== 'boolean')
    return null
  const words = toolName === CLINE_TOOL.SwitchToActMode ? CONTROL_DECISION_WORDS.plan : CONTROL_DECISION_WORDS.permission
  if (reply[CLINE_APPROVAL_REPLY.Approved] === true) {
    const mode = PLAN_MODE_WORDS.get(pickString(reply, CLINE_APPROVAL_REPLY.PermissionMode))
    return label(mode ? `${words.allow} (${mode})` : words.allow)
  }
  const reason = pickString(reply, CLINE_APPROVAL_REPLY.Reason).trim()
  return feedbackOrLabel(reason === CLINE_DECLINE_REASON.Tool ? '' : reason, words.deny)
}

function questionSummary(reply: Record<string, unknown>): ControlResponseSummary | null {
  if (reply[CLINE_CAPABILITY_REPLY.Ok] === true) {
    const answer = pickString(pickObject(reply, CLINE_CAPABILITY_REPLY.Payload), CLINE_CAPABILITY_REPLY.Result).trim()
    return label(answer || 'No answer')
  }
  if (reply[CLINE_CAPABILITY_REPLY.Ok] !== false)
    return null
  const error = pickString(reply, CLINE_CAPABILITY_REPLY.Error).trim()
  return feedbackOrLabel(error === CLINE_DECLINE_REASON.Question ? '' : error, 'Declined')
}
