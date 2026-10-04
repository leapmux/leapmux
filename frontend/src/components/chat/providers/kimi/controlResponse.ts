import type { ControlResponseSummary } from '../../model/controlResponse'
import type { PersistedControlResponse } from '../../persistedControlResponse'
import { KIMI_ANSWER_KIND, KIMI_APPROVAL_SCOPE, KIMI_DECISION, KIMI_DISPLAY, KIMI_EVENT, KIMI_GOAL_MODE, KIMI_PLAN_LABEL, KIMI_REPLY } from '~/generated/contracts/kimi-protocol'
import { isObject, pickObject, pickString, stringArray } from '~/lib/jsonPick'
import { CONTROL_DECISION_WORDS, feedbackOrLabel, joinAnswerLines, label } from '../../persistedControlResponse'
import { kimiQuestionRecords } from './askUserQuestion'

/** The names Kimi's own UI gives the permission modes a goal can start in. */
const KIMI_GOAL_MODE_LABELS: ReadonlyMap<string, string> = new Map([
  [KIMI_GOAL_MODE.Manual, 'Always Ask'],
  [KIMI_GOAL_MODE.Yolo, 'Ask When Needed'],
  [KIMI_GOAL_MODE.Auto, 'Never Ask'],
])

/**
 * The display of a saved Kimi Code answer.
 *
 * The saved row holds the answer the worker posted, in the server's own shape: an
 * approval's `decision` with its `scope`, `selected_label` and `feedback`, or a
 * question's `answers` keyed by question id. The stored request supplies the words
 * those ids stand for.
 */
export function kimiControlResponseSummary(cr: PersistedControlResponse): ControlResponseSummary | null {
  const native = pickObject(pickObject(cr.response, 'response'), 'response')
  if (!native)
    return null
  switch (pickString(cr.request, 'type')) {
    case KIMI_EVENT.ApprovalRequested:
      return kimiApprovalSummary(cr.request ?? {}, native)
    case KIMI_EVENT.QuestionRequested:
      return kimiQuestionSummary(cr.request ?? {}, native)
    default:
      return null
  }
}

function kimiApprovalSummary(request: Record<string, unknown>, native: Record<string, unknown>): ControlResponseSummary | null {
  const decision = pickString(native, KIMI_REPLY.Decision)
  const selected = pickString(native, KIMI_REPLY.SelectedLabel)
  const reason = pickString(native, KIMI_REPLY.Feedback).trim()
  const displayKind = pickString(pickObject(request, 'tool_input_display'), 'kind')
  if (decision === KIMI_DECISION.Cancelled)
    return label('Cancelled')
  if (displayKind === KIMI_DISPLAY.PlanReview) {
    if (decision === KIMI_DECISION.Approved)
      return label(selected ? `${CONTROL_DECISION_WORDS.plan.allow}: ${selected}` : CONTROL_DECISION_WORDS.plan.allow)
    if (decision !== KIMI_DECISION.Rejected)
      return null
    if (selected === KIMI_PLAN_LABEL.RejectAndExit)
      return label('Rejected and left plan mode')
    if (selected === KIMI_PLAN_LABEL.Revise)
      return feedbackOrLabel(reason, 'Requested revisions')
    return feedbackOrLabel(reason, CONTROL_DECISION_WORDS.plan.deny)
  }
  if (displayKind === KIMI_DISPLAY.GoalStart) {
    if (decision === KIMI_DECISION.Approved) {
      const mode = KIMI_GOAL_MODE_LABELS.get(selected)
      return label(mode ? `Started the goal in ${mode}` : 'Started the goal')
    }
    return decision === KIMI_DECISION.Rejected ? feedbackOrLabel(reason, 'Declined') : null
  }
  if (decision === KIMI_DECISION.Approved) {
    return label(pickString(native, KIMI_REPLY.Scope) === KIMI_APPROVAL_SCOPE.Session
      ? 'Allow for this session'
      : CONTROL_DECISION_WORDS.permission.allow)
  }
  return decision === KIMI_DECISION.Rejected ? feedbackOrLabel(reason, CONTROL_DECISION_WORDS.permission.deny) : null
}

function kimiQuestionSummary(request: Record<string, unknown>, native: Record<string, unknown>): ControlResponseSummary | null {
  if (native[KIMI_REPLY.Dismiss] === true)
    return label('Dismissed')
  const answers = pickObject(native, KIMI_REPLY.Answers)
  if (!answers)
    return null
  const lines: string[] = []
  for (const record of kimiQuestionRecords(request)) {
    const id = pickString(record, 'id')
    const answer = id && Object.hasOwn(answers, id) ? answers[id] : undefined
    if (!isObject(answer))
      continue
    const text = kimiAnswerText(record, answer)
    if (text)
      lines.push(`${pickString(record, 'question') || pickString(record, 'header') || id}: ${text}`)
  }
  const joined = joinAnswerLines(lines)
  return label(joined ?? 'No answer')
}

/** The words one answer states: the labels of the options it picked, and the reader's own text. */
function kimiAnswerText(question: Record<string, unknown>, answer: Record<string, unknown>): string {
  const options = Array.isArray(question.options) ? question.options.filter(isObject) : []
  const labelOf = (id: string) => pickString(options.find(option => pickString(option, 'id') === id), 'label') || id
  switch (pickString(answer, 'kind')) {
    case KIMI_ANSWER_KIND.Single:
      return labelOf(pickString(answer, 'option_id'))
    case KIMI_ANSWER_KIND.Multi:
      return stringArray(answer.option_ids).map(labelOf).join(', ')
    case KIMI_ANSWER_KIND.Other:
      return pickString(answer, 'text')
    case KIMI_ANSWER_KIND.MultiWithOther:
      return [...stringArray(answer.option_ids).map(labelOf), pickString(answer, 'other_text')].filter(Boolean).join(', ')
    case KIMI_ANSWER_KIND.Skipped:
      return 'Skipped'
    default:
      return ''
  }
}
