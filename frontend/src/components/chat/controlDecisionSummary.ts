import type { ElicitationRequest, PermissionOption, PlanChoice, ControlPrompt as PromptModel } from './model/controlPrompt'
import type { ControlResponseSummary } from './model/controlResponse'
import type { ControlQuestion } from './model/question'
import type {
  ControlDecision,
  DialogDecision,
  ElicitationDecision,
  PermissionDecision,
  PlanDecision,
  QuestionAnswer,
  QuestionDecision,
} from '~/generated/proto/leapmux/v1/agent_control_pb'
import { ACP_PERMISSION_KIND } from '~/generated/contracts/acp-protocol'
import { DialogAction, ElicitationAction } from '~/generated/proto/leapmux/v1/agent_control_pb'
import { prettifyJson } from '~/lib/jsonFormat'
import { isObject } from '~/lib/jsonPick'
import { createElicitationForm } from './controls/elicitationForm'
import { permissionOptionLabel } from './controls/permissionOptionLabels'
import { questionOptionValue } from './controls/types'
import { KIND_REJECT_ONCE } from './model/controlPrompt'

/**
 * The words of a saved answer, from the prompt that the request showed and the
 * decision that answered it.
 *
 * The Worker records both on the answer's transcript row, so this reads no
 * provider's request or answer format, and one answer reads the same for every
 * provider:
 *
 *   - A permission reads the chosen option.
 *   - A plan reads its approval or its refusal, the chosen choice, the
 *     permission mode that an approval switches to, and the feedback of a
 *     refusal.
 *   - A question reads each answered question with its title, its chosen options
 *     and its typed text, or the titles of the questions that it dismissed.
 *   - A form reads each field with its title.
 *   - A dialog reads its confirmation or its value.
 *
 * Feedback, the reason that a refusal states, replaces the words of the choice:
 * it says more. The Worker leaves out feedback that the runtime did not receive,
 * so a row never says that LeapMux sent a reason that went nowhere. It also
 * leaves out the feedback that it delivers as a message of its own, so the
 * transcript does not show that text twice.
 *
 * `permissionModeLabel` reads the agent's own label for one of its permission
 * modes, or undefined when the browser holds none, as for an agent that closed.
 * The row then names the mode by its value.
 *
 * Returns null for a decision that does not answer the prompt: another kind, or
 * an option or a choice that the prompt does not offer. The caller then shows a
 * generic word.
 */
export function controlDecisionSummary(
  prompt: PromptModel,
  decision: ControlDecision,
  permissionModeLabel: (mode: string) => string | undefined,
): ControlResponseSummary | null {
  const kind = decision.kind
  switch (kind.case) {
    case 'permission':
      return prompt.kind === 'permission'
        ? permissionSummary(prompt.permission.options, kind.value)
        : null
    case 'plan':
      return prompt.kind === 'plan'
        ? planSummary(prompt.choices ?? [], kind.value, permissionModeLabel)
        : null
    case 'question':
      return prompt.kind === 'question'
        ? questionSummary(prompt.questions, kind.value)
        : null
    case 'elicitation':
      return prompt.kind === 'elicitation'
        ? elicitationSummary(prompt.elicitation, kind.value)
        : null
    case 'dialog':
      return prompt.kind === 'dialog'
        ? dialogSummary(kind.value)
        : null
    default:
      return null
  }
}

/**
 * The words of a saved answer that its prompt does not state: the words of the
 * buttons that LeapMux draws for every provider, and of the outcomes that no
 * button names.
 */
const CONTROL_ANSWER_WORDS = {
  allow: 'Allow',
  deny: 'Deny',
  /** A refusal that also ends the agent's turn, as its button reads. */
  cancel: 'Cancel',
  approvePlan: 'Approve',
  rejectPlan: 'Reject',
  dismissQuestion: 'Dismissed',
  noAnswer: 'No answer',
  approveForm: 'Approved',
  rejectForm: 'Rejected',
  cancelled: 'Cancelled',
  confirmDialog: 'Approved',
  refuseDialog: 'Rejected',
  emptyValue: 'Empty answer',
} as const

function label(text: string): ControlResponseSummary {
  return { kind: 'label', text }
}

/** The reason of a refusal, when it states one, or else the words of the choice. */
function feedbackOrLabel(feedback: string, words: string): ControlResponseSummary {
  const reason = feedback.trim()
  return reason
    ? { kind: 'feedback', message: reason }
    : label(words)
}

/**
 * The words of one permission option: the runtime's own name for it, or else the
 * words of the button that sends it.
 *
 * An option names nothing when it carries no name, or carries its own id as its
 * name: LeapMux's own allow and deny (`agent.AllowDenyOptions` in Go) carry none,
 * and Goose and Oh My Pi name each option by its id. A one-time allow then reads
 * Allow, and a one-time refusal reads Deny, or Cancel when it also ends the agent's
 * turn, as their buttons do. A remembered one reads the label of its kind.
 */
function permissionOptionWords(option: PermissionOption): string {
  const named = option.name !== undefined && option.name !== option.optionId
  if (!named && option.kind === ACP_PERMISSION_KIND.AllowOnce)
    return CONTROL_ANSWER_WORDS.allow
  if (!named && option.kind === KIND_REJECT_ONCE) {
    return option.endsTurn
      ? CONTROL_ANSWER_WORDS.cancel
      : CONTROL_ANSWER_WORDS.deny
  }
  return permissionOptionLabel(option)
}

function permissionSummary(options: readonly PermissionOption[], decision: PermissionDecision): ControlResponseSummary | null {
  const option = options.find(candidate => candidate.optionId === decision.optionId)
  return option
    ? feedbackOrLabel(decision.feedback, permissionOptionWords(option))
    : null
}

/**
 * The words of a plan decision: the chosen choice, or else Approve or Reject, as
 * its button reads. An approval that switches the permission mode names the mode
 * after the words, by its label when the browser holds one and by its value
 * otherwise.
 */
function planSummary(
  choices: readonly PlanChoice[],
  decision: PlanDecision,
  permissionModeLabel: (mode: string) => string | undefined,
): ControlResponseSummary | null {
  let words: string
  if (decision.choiceId === '') {
    words = decision.approve
      ? CONTROL_ANSWER_WORDS.approvePlan
      : CONTROL_ANSWER_WORDS.rejectPlan
  }
  else {
    const choice = choices.find(candidate => candidate.id === decision.choiceId)
    if (!choice)
      return null
    words = choice.label
  }
  const mode = decision.approve
    ? decision.permissionMode
    : ''
  return feedbackOrLabel(decision.feedback, mode === ''
    ? words
    : `${words} (${permissionModeLabel(mode) || mode})`)
}

/**
 * The title of question number `index`: its text, else its header, else its
 * id, else its place. The question text is the one that every runtime states,
 * and a header is a short tag beside it.
 *
 * The title is one line. A runtime can state a question over several lines, and
 * a row that holds one title on each line would read such a title as several.
 */
function questionTitle(question: ControlQuestion, index: number): string {
  for (const title of [question.question, question.header, question.id]) {
    const line = (title ?? '').replace(/\s+/g, ' ').trim()
    if (line)
      return line
  }
  return `Question ${index + 1}`
}

/**
 * A dismissal without a reason, with the titles of the questions that it
 * dismissed: one title after the word, or one title on each line after it.
 */
function dismissalSummary(questions: readonly ControlQuestion[]): ControlResponseSummary {
  const titles = questions.map(questionTitle)
  switch (titles.length) {
    case 0:
      return label(CONTROL_ANSWER_WORDS.dismissQuestion)
    case 1:
      return label(`${CONTROL_ANSWER_WORDS.dismissQuestion}: ${titles[0]}`)
    default:
      return label([`${CONTROL_ANSWER_WORDS.dismissQuestion}:`, ...titles].join('\n'))
  }
}

/**
 * What one answer says: the labels of its chosen options, then its typed text,
 * which is one more answer to a multiple-choice question and a note beside the
 * selection where the prompt keeps one. A value that no option of the question
 * states reads as it stands.
 */
function answerValues(question: ControlQuestion, answer: QuestionAnswer): string[] {
  const values = answer.selected.map(value => question.options.find(option => questionOptionValue(option) === value)?.label ?? value)
  return [...values, answer.customText]
    .map(value => value.trim())
    .filter(value => value !== '')
}

function questionSummary(questions: readonly ControlQuestion[], decision: QuestionDecision): ControlResponseSummary {
  if (decision.cancelled) {
    const reason = decision.feedback.trim()
    return reason
      ? { kind: 'feedback', message: reason }
      : dismissalSummary(questions)
  }
  const lines = questions.flatMap((question, index) => {
    const answer = decision.answers[index]
    const values = answer
      ? answerValues(question, answer)
      : []
    return values.length > 0
      ? [`${questionTitle(question, index)}: ${values.join(', ')}`]
      : []
  })
  return label(lines.length > 0
    ? lines.join('\n')
    : CONTROL_ANSWER_WORDS.noAnswer)
}

/** The filled form of an accept, or undefined for none or for text that is no JSON object. */
function formContent(contentJson: string): Record<string, unknown> | undefined {
  if (contentJson === '')
    return undefined
  try {
    const content: unknown = JSON.parse(contentJson)
    return isObject(content)
      ? content
      : undefined
  }
  catch {
    return undefined
  }
}

/**
 * One line for each field of an accepted form, under its title in the form's
 * schema. A value that one of the field's options states reads as that option's
 * label, and any other value reads as its text.
 */
function elicitationSummary(request: ElicitationRequest, decision: ElicitationDecision): ControlResponseSummary | null {
  switch (decision.action) {
    case ElicitationAction.DECLINE:
      return label(CONTROL_ANSWER_WORDS.rejectForm)
    case ElicitationAction.CANCEL:
      return label(CONTROL_ANSWER_WORDS.cancelled)
    case ElicitationAction.ACCEPT:
      break
    default:
      return null
  }
  const lines: string[] = [CONTROL_ANSWER_WORDS.approveForm]
  const content = formContent(decision.contentJson)
  if (content) {
    const fields = new Map(createElicitationForm(request.schema).fields.map(field => [field.key, field]))
    for (const [key, value] of Object.entries(content)) {
      const field = fields.get(key)
      const option = field?.options.find(candidate => candidate.value === JSON.stringify(value))
      const text = option?.label ?? (typeof value === 'string'
        ? value
        : prettifyJson(value).trimEnd())
      lines.push(`${field?.label || key}: ${text}`)
    }
  }
  return label(lines.join('\n'))
}

/** A dialog's confirmation, its refusal, its cancellation, or the value it sent, exactly. */
function dialogSummary(decision: DialogDecision): ControlResponseSummary | null {
  switch (decision.action) {
    case DialogAction.CONFIRM:
      return label(CONTROL_ANSWER_WORDS.confirmDialog)
    case DialogAction.REFUSE:
      return label(CONTROL_ANSWER_WORDS.refuseDialog)
    case DialogAction.CANCEL:
      return label(CONTROL_ANSWER_WORDS.cancelled)
    case DialogAction.SUBMIT:
      return label(decision.value === ''
        ? CONTROL_ANSWER_WORDS.emptyValue
        : decision.value)
    default:
      return null
  }
}
