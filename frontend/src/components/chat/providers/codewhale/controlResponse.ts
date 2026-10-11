import type { ControlResponseSummary } from '../../model/controlResponse'
import type { PersistedControlResponse } from '../../persistedControlResponse'
import { CODEWHALE_ANSWER_TEXT, CODEWHALE_DECISION, CODEWHALE_REPLY_FIELD, CODEWHALE_REPLY_FRAME } from '~/generated/contracts/codewhale-protocol'
import { pickString } from '~/lib/jsonPick'
import { CONTROL_DECISION_WORDS, label } from '../../persistedControlResponse'
import { codewhaleAnswerValues, codewhaleQuestionRecords, codewhaleQuestionText } from './askUserQuestion'

/** The word a declined question shows. The runtime offers no decline, so LeapMux words it. */
const DECLINED_WORD = 'Declined'

/**
 * The saved answer of one Codewhale control request.
 *
 * The saved response is the REPLY FRAME the worker built and posted -- `{frame:
 * "approval", approval_id, decision}` or `{frame: "user_input", thread_id, input_id,
 * answers, declined?}` -- so it states exactly what reached the runtime.
 *
 * An approval reads the words its own buttons carried, Allow and Deny. A deny reason
 * does not reach the runtime's approval route: the worker sends it as the reader's
 * next message, which the transcript draws as its own row.
 *
 * A question reads one `question: answer` line for each question the answers
 * address, in the order the runtime asked them. A decline answers every question with
 * the same text, which reads as the decline and its reason.
 */
export function codewhaleControlResponseSummary(cr: PersistedControlResponse): ControlResponseSummary | null {
  const response = cr.response
  switch (pickString(response, CODEWHALE_REPLY_FIELD.Frame)) {
    case CODEWHALE_REPLY_FRAME.Approval:
      switch (pickString(response, CODEWHALE_REPLY_FIELD.Decision)) {
        case CODEWHALE_DECISION.Allow:
          return label(CONTROL_DECISION_WORDS.permission.allow)
        case CODEWHALE_DECISION.Deny:
          return label(CONTROL_DECISION_WORDS.permission.deny)
        default:
          return null
      }
    case CODEWHALE_REPLY_FRAME.UserInput:
      return questionAnswerDisplay(cr)
    default:
      return null
  }
}

function questionAnswerDisplay(cr: PersistedControlResponse): ControlResponseSummary | null {
  const answers = cr.response?.[CODEWHALE_REPLY_FIELD.Answers]
  const questions = codewhaleQuestionRecords(cr.request ?? {})
  if (cr.response?.[CODEWHALE_REPLY_FIELD.Declined] === true) {
    // Every question carries the same refusal, so the first one states it.
    const reason = questions.map(question => codewhaleAnswerValues(answers, pickString(question, 'id'))[0]).find(Boolean) ?? ''
    return label(reason && reason !== CODEWHALE_ANSWER_TEXT.Declined ? `${DECLINED_WORD}\n${reason}` : DECLINED_WORD)
  }
  const lines = questions.flatMap((question) => {
    const text = codewhaleQuestionText(question)
    const values = codewhaleAnswerValues(answers, pickString(question, 'id'))
    return text && values.length > 0 ? [`${text}: ${values.join(', ')}`] : []
  })
  return lines.length > 0 ? label(lines.join('\n')) : null
}
