/**
 * The capture for the plan file path that Kimi Code chooses at random.
 *
 * Kimi states the path only in the plan-mode system reminder
 * (`Plan file: <path>`), and its `ExitPlanMode` raises the plan from that file.
 * A scripted `Write` to `{{planFile}}` puts the plan there. The path always
 * ends in `.md`, which keeps the match off the closing tag of the reminder.
 */
export const KIMI_PLAN_FILE_CAPTURE = { planFile: 'Plan file: (\\S+?\\.md)' } as const

/**
 * How many times `needle` occurs in `text`.
 *
 * A model request repeats the whole conversation, so a marker that an earlier
 * message holds is in every later request body. Compare the counts of two
 * bodies to prove that the messages between them hold the marker.
 */
export function occurrences(text: string, needle: string): number {
  if (needle === '')
    throw new Error('occurrences needs a needle that is not empty')
  return text.split(needle).length - 1
}
