import type { ControlResponseSummary } from '../../model/controlResponse'
import type { ControlDecisionWords, PersistedControlResponse } from '../../persistedControlResponse'
import type { DroidPermissionOption } from '~/generated/contracts/droid-protocol'
import { DROID_ASK_USER_FIELD, DROID_PERMISSION_OPTION, DROID_REPLY } from '~/generated/contracts/droid-protocol'
import { isObject, pickObject, pickString } from '~/lib/jsonPick'
import { controlDecisionWords, joinAnswerLines, label, labeledAnswerLine } from '../../persistedControlResponse'
import { droidExtractControl } from './extractControl'

/**
 * The word that a cancelled questionnaire shows. Droid's reply states `cancelled` and no
 * reason. A reason that the reader typed follows as the reader's next message.
 */
const CANCELLED_WORD = 'Cancelled'

/** The words that an answered questionnaire shows when no answer holds text. */
const NO_ANSWER_WORDS = 'No answer'

/** The words of an option that approves and also sets Droid's autonomy level. */
function withAutonomyLevel(words: ControlDecisionWords, level: string): string {
  return `${words.allow} and set the autonomy level to ${level}`
}

/** The words of an option that approves the spec and implements it in a new session. */
function inNewSession(words: ControlDecisionWords, level: string): string {
  return `${words.allow} in a new session at autonomy level ${level}`
}

/**
 * The words that each `selectedOption` of a saved permission answer shows.
 *
 * Each label starts with the word of the button that the control drew: Approve or
 * Reject for the spec review, and Allow or Deny for every other permission. The two
 * buttons reach Droid as `proceed_once` and `cancel`, because the worker turns allow
 * and deny into them (`droidResolveControlResponse`). The worker also forwards another
 * option that the browser's decision states, when the request offered it, so every
 * option has its words.
 * The rest of each label states what Droid does beyond the approval, as the Factory
 * Droid SDK describes the option.
 */
const DROID_OPTION_WORDS = {
  [DROID_PERMISSION_OPTION.ProceedOnce]: words => words.allow,
  // Droid saves a rule. For a file, the rule covers the parent directory.
  [DROID_PERMISSION_OPTION.ProceedAlways]: words => `${words.allow} always`,
  [DROID_PERMISSION_OPTION.ProceedAlwaysFile]: words => `${words.allow} always for this file`,
  [DROID_PERMISSION_OPTION.ProceedAlwaysTools]: words => `${words.allow} always for these MCP tools`,
  [DROID_PERMISSION_OPTION.ProceedAlwaysServer]: words => `${words.allow} always for this MCP server`,
  [DROID_PERMISSION_OPTION.ProceedAutoRun]: words => `${words.allow} and raise the autonomy level`,
  [DROID_PERMISSION_OPTION.ProceedAutoRunLow]: words => withAutonomyLevel(words, 'Low'),
  [DROID_PERMISSION_OPTION.ProceedAutoRunMedium]: words => withAutonomyLevel(words, 'Medium'),
  [DROID_PERMISSION_OPTION.ProceedAutoRunHigh]: words => withAutonomyLevel(words, 'High'),
  [DROID_PERMISSION_OPTION.ProceedNewSession]: words => `${words.allow} in a new session`,
  [DROID_PERMISSION_OPTION.ProceedNewSessionLow]: words => inNewSession(words, 'Low'),
  [DROID_PERMISSION_OPTION.ProceedNewSessionMedium]: words => inNewSession(words, 'Medium'),
  [DROID_PERMISSION_OPTION.ProceedNewSessionHigh]: words => inNewSession(words, 'High'),
  // Droid requires `editedSpecContent` beside this option and implements that spec.
  [DROID_PERMISSION_OPTION.ProceedEdit]: words => `${words.allow} with an edited spec`,
  // Droid Shield: Droid proceeds once and reports its findings as a false positive.
  [DROID_PERMISSION_OPTION.ProceedReportFalsePositive]: words => `${words.allow} and report a false positive`,
  [DROID_PERMISSION_OPTION.Cancel]: words => words.deny,
} as const satisfies Record<DroidPermissionOption, (words: ControlDecisionWords) => string>

/**
 * Whether a stored `selectedOption` is one that Droid defines.
 *
 * `Object.hasOwn`, never a bare lookup: the option comes off the wire, and one that
 * spells an `Object.prototype` member such as `toString` would find a function there.
 */
function isDroidPermissionOption(option: string): option is DroidPermissionOption {
  return Object.hasOwn(DROID_OPTION_WORDS, option)
}

/**
 * The display of one saved Factory Droid answer.
 *
 * The saved row holds the JSON-RPC response that the worker wrote to Droid's stdin
 * (`droidResolveControlResponse`), not the neutral envelope that the browser sent. Its
 * `result` is Droid's own reply: `{selectedOption}` for a permission request, and
 * `{cancelled, answers}` for a question request. The reply itself shows which of the
 * two it answers, so a row whose stored request is absent still reads correctly.
 *
 * Neither reply carries the reason for a rejection: the worker sends that reason as the
 * reader's next message, which the transcript shows as a row of its own.
 *
 * Null for a reply that is neither. The caller then falls back to the shared display,
 * which reads the neutral envelope or shows the generic label.
 */
export function droidControlResponseSummary(cr: PersistedControlResponse): ControlResponseSummary | null {
  const result = pickObject(cr.response, 'result')
  if (!result)
    return null
  const option = result[DROID_REPLY.SelectedOption]
  if (typeof option === 'string')
    return permissionDisplay(cr.request, option)
  const answers = result[DROID_REPLY.Answers]
  if (Array.isArray(answers))
    return questionDisplay(result, answers)
  return null
}

function permissionDisplay(request: Record<string, unknown> | undefined, option: string): ControlResponseSummary | null {
  if (!isDroidPermissionOption(option))
    return null
  // The control that the banner drew decides the pair of words. An absent request
  // cannot state a spec review, so it takes the permission words.
  return label(DROID_OPTION_WORDS[option](controlDecisionWords(droidExtractControl({ payload: request ?? {} }))))
}

/**
 * The answers of one reply, one line each, in their stored order.
 *
 * The worker writes the answers in the order of the questions, each with the index and
 * the words of its question (`droidAnswersInQuestionOrder`). Droid reports them to the
 * model in that same order, so the row shows what Droid received.
 */
function questionDisplay(result: Record<string, unknown>, answers: unknown[]): ControlResponseSummary {
  if (result[DROID_REPLY.Cancelled] === true)
    return label(CANCELLED_WORD)
  const lines = answers.filter(isObject).flatMap((entry) => {
    const question = pickString(entry, DROID_ASK_USER_FIELD.Question).trim()
    const line = question ? labeledAnswerLine(question, [pickString(entry, DROID_ASK_USER_FIELD.Answer)]) : null
    return line ? [line] : []
  })
  return label(joinAnswerLines(lines) ?? NO_ANSWER_WORDS)
}
