import type { MockModelRule } from '../helpers/mockModelScript'
import { MOCK_SESSION_TITLE } from '../helpers/mockModelScenario'

/** The phrase that identifies the system prompt of the Junie capability filter. */
const CAPABILITY_FILTER = 'capability filter agent'

/**
 * The turns that Junie runs for itself, at times that no test controls.
 *
 * - The capability filter routes a request to the capabilities that it needs. An empty answer selects none.
 * - The task description summarizer names the task.
 * - The task summarizer summarizes a child task when the child ends. Junie reads the `<summary>` and `<title>`
 *   tags of the answer.
 *
 * The Junie test object registers these rules for every test (`junie-fixtures.ts`), so a spec registers no copy of
 * them. Each rule has high priority, as `HOUSEKEEPING_RULES` in `helpers/mockModelScenario.ts` states, so a test rule
 * that matches the same task text cannot take the request. A spec that needs another capability answer registers
 * {@link junieCapabilityAnswer}.
 */
export const JUNIE_HOUSEKEEPING_RULES: readonly MockModelRule[] = [
  { name: 'junie-capability-filter', priority: 'high', when: { system: CAPABILITY_FILTER }, respond: { text: '' } },
  { name: 'junie-task-name', priority: 'high', when: { system: 'task description summarizer' }, respond: { text: MOCK_SESSION_TITLE } },
  {
    name: 'junie-task-summary',
    priority: 'high',
    when: { system: 'You are a task summarizer' },
    respond: { text: `<summary>The child task completed.</summary><title>${MOCK_SESSION_TITLE}</title>` },
  },
]

/**
 * A rule that answers the Junie capability filter with `selection`, such as `'1'` for the first listed MCP tool.
 *
 * The rule has the high priority of {@link JUNIE_HOUSEKEEPING_RULES}. A spec registers it after the test object
 * registers those rules, and a newer rule of the same priority matches first, so it replaces the empty answer.
 * `name` must differ from every other rule of the script.
 */
export function junieCapabilityAnswer(name: string, selection: string): MockModelRule {
  return { name, priority: 'high', when: { system: CAPABILITY_FILTER }, respond: { text: selection } }
}
