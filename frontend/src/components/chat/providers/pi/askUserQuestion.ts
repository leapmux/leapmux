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
import { pickString } from '~/lib/jsonPick'
import { PI_DIALOG_METHOD } from './protocol'

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
 * by both `piPlugin.extractAskUserQuestions` (registry surface) and
 * `PiControlContent` / `PiControlActions` (controls bubble), so a single
 * source of truth defines the question id, prompt, and options for any
 * given Pi payload.
 */
export function piQuestionsFromPayload(payload: Record<string, unknown>): Question[] {
  const method = pickString(payload, 'method')
  if (method === PI_DIALOG_METHOD.Select) {
    return [{
      id: pickString(payload, 'id'),
      question: pickString(payload, 'title') || 'Choose an option',
      options: piSelectOptions(payload),
    }]
  }
  return [{
    id: pickString(payload, 'id'),
    question: pickString(payload, 'title') || 'Enter a value',
    options: [],
  }]
}
