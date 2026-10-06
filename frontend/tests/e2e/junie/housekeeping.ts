import type { MockModelRule } from '../helpers/mockModelScript'
import { MOCK_SESSION_TITLE } from '../helpers/mockModelScenario'

/**
 * The turns that Junie runs for itself, at times that no test controls.
 *
 * - The capability filter routes a request to the capabilities that it needs. An empty answer selects none.
 * - The task description summarizer names the task.
 * - The task summarizer summarizes a child task when the child ends. Junie reads the `<summary>` and `<title>`
 *   tags of the answer.
 *
 * The Junie test object registers these rules for every test (`junie-fixtures.ts`), so a spec registers no copy of
 * them. A spec that needs another capability answer, such as `'1'` for an MCP tool, registers its own rule under
 * another name: a newer rule of the same priority matches first.
 */
export const JUNIE_HOUSEKEEPING_RULES: readonly MockModelRule[] = [
  { name: 'junie-capability-filter', when: { system: 'capability filter agent' }, respond: { text: '' } },
  { name: 'junie-task-name', when: { system: 'task description summarizer' }, respond: { text: MOCK_SESSION_TITLE } },
  {
    name: 'junie-task-summary',
    when: { system: 'You are a task summarizer' },
    respond: { text: `<summary>The child task completed.</summary><title>${MOCK_SESSION_TITLE}</title>` },
  },
]
