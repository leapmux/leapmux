/**
 * The capture for the plan file path that Kimi Code chooses at random.
 *
 * Kimi states the path only in the plan-mode system reminder
 * (`Plan file: <path>`), and its `ExitPlanMode` raises the plan from that file.
 * A scripted `Write` to `{{planFile}}` puts the plan there. The path always
 * ends in `.md`, which keeps the match off the closing tag of the reminder.
 */
export const KIMI_PLAN_FILE_CAPTURE = { planFile: 'Plan file: (\\S+?\\.md)' } as const

/** The latest native Plan-mode disclosure, or undefined when no disclosure exists. */
export function kimiPlanModeFromInstructions(text: string): boolean | undefined {
  let active: boolean | undefined
  for (const match of text.matchAll(/Plan mode is (no longer )?active[.:]/g))
    active = match[1] === undefined
  return active
}
