import type { PermissionOption } from '../../model/controlPrompt'
import type { ControlResponseSummary } from '../../model/controlResponse'
import type { ControlQuestion } from '../../model/question'
import type { PersistedControlResponse } from '../../persistedControlResponse'
import { GROK_PLAN_OUTCOME, GROK_QUESTION_OUTCOME, GROK_REPLY_FIELD } from '~/generated/contracts/grok-protocol'
import { MCP_ELICITATION_ACTION } from '~/generated/contracts/mcp-elicitation'
import { pickObject, pickString } from '~/lib/jsonPick'
import { elicitationResponseSummary } from '../../controls/elicitationResponse'
import { permissionOptionLabel } from '../../controls/permissionOptionLabels'
import { CONTROL_DECISION_WORDS, feedback, feedbackOrLabel, label, labelOrNull } from '../../persistedControlResponse'
import { acpControlResponseSummary } from '../acp/controlResponse'
import { grokQuestionAnswerLines } from './askUserQuestion'

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

/** The display of one saved question answer, read through the questions of the recorded prompt. */
function grokQuestionDisplay(questions: readonly ControlQuestion[], result: Record<string, unknown>): ControlResponseSummary | null {
  switch (pickString(result, GROK_REPLY_FIELD.Outcome)) {
    case GROK_QUESTION_OUTCOME.Accepted:
      return labelOrNull(grokQuestionAnswerLines(questions, result))
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
 * field back here. The shared display reads the form itself from the prompt that the
 * Worker recorded.
 */
function grokElicitationDisplay(cr: PersistedControlResponse, result: Record<string, unknown>): ControlResponseSummary | null {
  const outcome = pickString(result, GROK_REPLY_FIELD.Outcome)
  if (!(Object.values(MCP_ELICITATION_ACTION) as string[]).includes(outcome))
    return null
  const { [GROK_REPLY_FIELD.Outcome]: _outcome, ...rest } = result
  return elicitationResponseSummary({
    ...cr,
    response: {
      ...cr.response,
      result: { ...rest, action: outcome },
    },
  })
}

/**
 * The display of one saved permission.
 *
 * Two requests share the permission prompt, and the reply tells them apart:
 *
 *   - Grok's folder-trust question takes Grok's own reply word as its `outcome`, and
 *     the Worker offers those words as the option ids (`grokFolderTrustOptions` in Go),
 *     so the word reads back as the option of the recorded prompt.
 *   - A tool permission takes the protocol's selection, which the shared permission
 *     display reads. A rejection that carried a reason shows the reason, which is what
 *     the reader typed.
 */
function grokPermissionDisplay(cr: PersistedControlResponse, result: Record<string, unknown>, options: readonly PermissionOption[]): ControlResponseSummary | null {
  const word = pickString(result, GROK_REPLY_FIELD.Outcome)
  if (word) {
    const option = options.find(candidate => candidate.optionId === word)
    return option
      ? label(permissionOptionLabel(option))
      : null
  }
  const reason = pickString(pickObject(result, '_meta'), GROK_REPLY_FIELD.FollowupMessage).trim()
  return reason
    ? feedback(reason)
    : acpControlResponseSummary(cr)
}

/**
 * The display of one saved Grok answer. It dispatches on the prompt that the Worker
 * recorded with the answer, which states the request that the reader answered. A row
 * that records no prompt reads as nothing, so the caller falls back to the shared
 * label.
 */
export function grokControlResponseSummary(cr: PersistedControlResponse): ControlResponseSummary | null {
  const result = pickObject(cr.response, 'result')
  if (!result)
    return null
  switch (cr.prompt?.kind) {
    case 'plan':
      return grokPlanDisplay(result)
    case 'question':
      return grokQuestionDisplay(cr.prompt.questions, result)
    case 'elicitation':
      return grokElicitationDisplay(cr, result)
    case 'permission':
      return grokPermissionDisplay(cr, result, cr.prompt.permission.options)
    default:
      return null
  }
}
