import type { TodoItem } from '~/models/todo'
import { rawTodosToItems } from '~/models/todo'

/**
 * The to-do rows a Copilot `update_todo` argument carries.
 *
 * Copilot's own tool schema describes that argument as "a markdown checklist of TODO
 * items showing completed and pending tasks", so the checklist is the runtime's own
 * statement of the list. Only the two markers the markdown task-list syntax defines
 * are read. Every other marker keeps its item pending, which is the answer an
 * unrecognized status word already gets: a state this build cannot understand must
 * not be shown as finished.
 *
 * The Go worker parses the same text for the to-do sidebar. The two carry one shared
 * corpus, `testdata/copilot_checklist_conformance.json`, which both suites replay.
 */
export function copilotChecklistItems(checklist: string): TodoItem[] {
  const rows: Array<{ content: string, status: string, activeForm: string }> = []
  for (const line of checklist.split('\n')) {
    // The item text starts at a non-space, so the two whitespace runs cannot
    // exchange characters and the match stays linear.
    const item = /^[ \t]*[-*+][ \t]+\[(.)\][ \t]+(\S.*)?$/.exec(line.replace(/\r$/, ''))
    if (!item)
      continue
    const content = (item[2] ?? '').trim()
    if (!content)
      continue
    const status = item[1] === 'x' || item[1] === 'X' ? 'completed' : 'pending'
    rows.push({ content, status, activeForm: content })
  }
  return rawTodosToItems(rows)
}
