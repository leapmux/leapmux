import type { ControlResponseSummary } from '../../model/controlResponse'
import type { PersistedControlResponse } from '../../persistedControlResponse'
import { GROK_METHOD, GROK_PLAN_OUTCOME, GROK_QUESTION_OUTCOME, GROK_REPLY_FIELD } from '~/generated/contracts/grok-protocol'
import { MCP_ELICITATION_ACTION } from '~/generated/contracts/mcp-elicitation'
import { pickObject, pickString } from '~/lib/jsonPick'
import { withElicitationResponse } from '../../controls/elicitationResponse'
import { CONTROL_DECISION_WORDS, feedback, feedbackOrLabel, label, labelOrNull } from '../../persistedControlResponse'
import { acpControlResponseSummary } from '../acp/controlResponse'
import { grokQuestionAnswerLines } from './askUserQuestion'
import { grokElicitation, grokFolderTrustLabel } from './extractControl'

/**
 * Adds the reason of a rejected permission to Grok's reply.
 *
 * Grok reads it from `_meta.followup_message` of a `reject_once` answer: it gives
 * the text to the model and the turn goes on, where a bare rejection ends the turn.
 */
export function grokPermissionRejectReason(result: Record<string, unknown>, reason: string): Record<string, unknown> {
  const meta = pickObject(result, '_meta') ?? {}
  return { ...result, _meta: { ...meta, [GROK_REPLY_FIELD.FollowupMessage]: reason } }
}

/** The display of one saved plan approval, from the reply the worker sent Grok. */
function grokPlanDisplay(result: Record<string, unknown>): ControlResponseSummary | null {
  switch (pickString(result, GROK_REPLY_FIELD.Outcome)) {
    case GROK_PLAN_OUTCOME.Approved:
      return label(CONTROL_DECISION_WORDS.plan.allow)
    case GROK_PLAN_OUTCOME.Cancelled:
      return feedbackOrLabel(pickString(result, GROK_REPLY_FIELD.Feedback).trim(), CONTROL_DECISION_WORDS.plan.deny)
    default:
      return null
  }
}

/** The display of one saved question answer. */
function grokQuestionDisplay(request: Record<string, unknown> | undefined, result: Record<string, unknown>): ControlResponseSummary | null {
  switch (pickString(result, GROK_REPLY_FIELD.Outcome)) {
    case GROK_QUESTION_OUTCOME.Accepted:
      return labelOrNull(grokQuestionAnswerLines(request, result))
    case GROK_QUESTION_OUTCOME.Cancelled:
      return label('Cancel')
    default:
      return null
  }
}

/**
 * The display of one saved MCP form.
 *
 * Grok reads MCP's answer under `outcome` rather than MCP's own `action`, so the
 * worker renamed the field. The shared form display reads `action`, and it gets the
 * field back here.
 */
function grokElicitationDisplay(cr: PersistedControlResponse, result: Record<string, unknown>): ControlResponseSummary | null {
  const outcome = pickString(result, GROK_REPLY_FIELD.Outcome)
  if (!(Object.values(MCP_ELICITATION_ACTION) as string[]).includes(outcome))
    return null
  const { [GROK_REPLY_FIELD.Outcome]: _outcome, ...rest } = result
  return withElicitationResponse(grokElicitation, () => null)({ ...cr, response: { ...cr.response, result: { ...rest, action: outcome } } })
}

/**
 * The display of one saved Grok answer. It dispatches on the request, and a request
 * the reader answered with an option of its own takes the shared permission display.
 *
 * A rejection that carried a reason shows the reason, which is what the reader typed.
 */
export function grokControlResponseSummary(cr: PersistedControlResponse): ControlResponseSummary | null {
  const result = pickObject(cr.response, 'result')
  if (!result)
    return acpControlResponseSummary(cr)
  switch (pickString(cr.request, 'method')) {
    case GROK_METHOD.ExitPlanMode:
      return grokPlanDisplay(result)
    case GROK_METHOD.AskUserQuestion:
      return grokQuestionDisplay(cr.request, result)
    case GROK_METHOD.McpElicit:
      return grokElicitationDisplay(cr, result)
    case GROK_METHOD.FolderTrust:
      return labelOrNull(grokFolderTrustLabel(pickString(result, GROK_REPLY_FIELD.Outcome)) ?? null)
    default: {
      const reason = pickString(pickObject(result, '_meta'), GROK_REPLY_FIELD.FollowupMessage).trim()
      return reason ? feedback(reason) : acpControlResponseSummary(cr)
    }
  }
}
