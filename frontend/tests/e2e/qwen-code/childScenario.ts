import type { MockModelMatcher } from '../helpers/mockModelScript'
import { childTaskAfter } from '../helpers/runningChildProof'
import { HELD_CHILD_TASK } from '../helpers/subagentRegistry'

/**
 * The startup reminders that Qwen Code puts before the task, as a regular expression source: zero or more
 * `<system-reminder>` blocks, each with the whitespace after it. A block ends at its first close tag, because Qwen Code
 * escapes each reminder tag inside the body of a reminder (`escapeSystemReminderTags`).
 */
const QWEN_STARTUP_REMINDERS = '(?:<system-reminder>(?:(?!</system-reminder>)[\\s\\S])*</system-reminder>\\s*)*'

/**
 * Match the own turn of a Qwen Code child from its task.
 *
 * Qwen Code sends the `prompt` of the spawn call, unchanged, as the task of the child. A workflow `agent()` call does
 * the same. The facts below are from Qwen Code 0.24.7:
 *
 * - Qwen Code keeps its startup reminders in a user entry of their own, before the task (`getInitialChatHistory`).
 * - Before each request, `extractCuratedHistory` merges consecutive user entries (`appendCuratedContent`). The
 *   reminders and the task thus reach the model as text parts of ONE user message, and the task is its last part.
 * - Each tool result is a `tool` message. A later turn of the child thus keeps that user message as its last one,
 *   unless a tool result adds a text or media part, which Qwen Code sends as a user message after the result.
 *
 * The last user text of each child turn is therefore the reminders, then the task. The matcher anchors the task right
 * after the reminders. A turn that quotes the task after other text, or inside a reminder, does not match.
 */
export function qwenChildTurn(task: string): MockModelMatcher {
  return childTaskAfter(QWEN_STARTUP_REMINDERS, task)
}

/** The turn of the held child of `openHeldChildTab`, which the interrupt and send cells hold. */
export const QWEN_HELD_CHILD_TURN: MockModelMatcher = qwenChildTurn(HELD_CHILD_TASK)
