/**
 * Pi extension_ui_request → AskUserQuestion adapters.
 *
 * Pi surfaces multiple-choice prompts via `extension_ui_request` with
 * `method: "select"` and a flat `options: string[]`. The shared
 * AskUserQuestion components (used by Claude / Codex / OpenCode) expect
 * a richer `Question[]` shape with per-option `{label, description?}`
 * objects. The conversion lives here so the plugin (registry-side) and
 * the controls-bubble UI never drift on the option-shape mapping.
 */

import type { Question } from '../../controls/types'
import type { ParsedMessageContent } from '~/lib/messageParser'
import { PI_DIALOG_METHOD } from '~/generated/contracts/pi-protocol'
import { pickString } from '~/lib/jsonPick'
import { piQuestionFromSource, piQuestionOptionLine } from './questionSource'

/**
 * Convert Pi's flat `options: string[]` into the labelled-option shape
 * the AskUserQuestion renderer consumes. Non-string entries are dropped
 * defensively — Pi's wire format only emits strings, but the chat
 * payload may have been tampered with on disk.
 */
export function piSelectOptions(payload: Record<string, unknown>): Array<{ label: string }> {
  const options = payload.options
  return Array.isArray(options)
    ? options.flatMap(option => typeof option === 'string' ? [{ label: option }] : [])
    : []
}

/**
 * Build the canonical Question[] for a Pi `extension_ui_request`. Used
 * by both `piPlugin.askUserQuestion.extractQuestions` (registry surface) and
 * `PiControlContent` / `PiControlActions` (controls bubble), so a single
 * source of truth defines the question id, prompt, and options for any
 * given Pi payload.
 */
export function piQuestionsFromPayload(payload: Record<string, unknown>, source?: ParsedMessageContent): Question[] {
  const method = pickString(payload, 'method')
  const question = piQuestionFromSource(payload, source)
  if (question && method === PI_DIALOG_METHOD.Input && question.multiSelect) {
    return [{
      id: pickString(payload, 'id'),
      question: question.prompt,
      header: question.header || undefined,
      multiSelect: true,
      allowEmpty: true,
      options: question.options.map((option, index) => ({ ...option, value: String(index + 1) })),
    }]
  }
  if (method === PI_DIALOG_METHOD.Select) {
    return [{
      id: pickString(payload, 'id'),
      question: question?.prompt ?? (pickString(payload, 'title') || 'Choose an option'),
      ...(question?.header ? { header: question.header } : {}),
      options: question
        ? question.options.map((option, index) => ({ ...option, value: piQuestionOptionLine(option, index) }))
        : piSelectOptions(payload),
    }]
  }
  return [{
    id: pickString(payload, 'id'),
    question: question?.title ?? (pickString(payload, 'title') || 'Enter a value'),
    options: [],
    ...(method === PI_DIALOG_METHOD.Input ? { allowEmpty: true } : {}),
  }]
}
