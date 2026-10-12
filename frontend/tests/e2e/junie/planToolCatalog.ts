/**
 * The tools that Junie's Default mode offers and its Plan mode always withholds.
 *
 * A probe of Junie 26.9.22 listed the tools of every request in the situations of the specs:
 *
 * - Default mode offers 16 tools: `open`, `open_entire_file`, `scroll_down`, `scroll_up`, `ask_user`,
 *   `glob_search`, `grep_search`, `bash`, `answer`, `search_replace`, `multi_edit`, `create`,
 *   `undo_edit`, `submit`, `spawn_subagent`, and `agent_skill_read_doc`.
 * - Plan mode offers 9 tools while no plan exists: `open`, `open_entire_file`, `submit`, `ask_user`,
 *   `answer`, `glob_search`, `grep_search`, `bash`, and `agent_skill_read_doc`.
 * - Plan mode offers `multi_edit` also while a plan exists, so after a denied plan or an approved plan.
 *   `StandalonePlanActionsResolver` adds it only when the plan context holds a proposal. Its schema
 *   equals the Default schema. The planner prompt restricts it: "The file editing tool may ONLY be
 *   used to edit the plan file". Junie then asks for `submit` without parameters.
 *
 * `multi_edit` is therefore not in this list. Plan mode withholds each other tool that changes a file,
 * the tool that starts a subagent, and the two scroll tools, in every situation of the probe. `submit`
 * and `answer` are in both catalogs.
 */
export const PLAN_MODE_WITHHELD_TOOLS = ['search_replace', 'create', 'undo_edit', 'spawn_subagent', 'scroll_down', 'scroll_up'] as const

/** Return the tools of a catalog that Plan mode withholds. A Plan catalog returns an empty list. */
export function planWithheldToolsOffered(tools: readonly string[]): string[] {
  return PLAN_MODE_WITHHELD_TOOLS.filter(tool => tools.includes(tool))
}
