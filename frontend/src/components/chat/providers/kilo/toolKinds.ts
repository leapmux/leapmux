import type { ToolKind } from '../../model/toolKind'

/**
 * The kind of each tool Kilo adds on top of the OpenCode tool set.
 *
 * Kilo's own protocol layer answers `other` for every one of them (`toToolKind` in
 * `packages/opencode/src/acp/tool.ts` lists six kinds and nothing else), so each of
 * these rows reached the reader as the generic wrench above its raw arguments. The
 * kind is what four separate tables read -- the icon, the label, the title and the
 * input summary -- so this table is what makes the row state its own work.
 *
 * Every tool Kilo adds is listed, and `kilo/toolKinds.test.ts` fails the suite when
 * one is not: a tool this table skips keeps the `other` kind, whose wrench and whose
 * raw-argument dump identify nothing the agent ran.
 *
 * Kilo's `context7_*` pair is absent for the opposite reason -- Kilo already answers
 * `search` for both, so this table would only repeat what the frame states.
 */
export const KILO_TOOL_KINDS: Readonly<Record<string, ToolKind>> = {
  // The agent manager's own surface, which lists what it can run. `list` would
  // title the row with a PATH it has none of.
  agent_manager_models: 'agents',
  background_process: 'execute',
  // The board and the memory store are both the agent's own notes, kept across
  // turns. Reading one is not a file read and writing one is not a file write, so
  // neither half takes a file kind whose title renderer wants a path.
  board_post: 'memory',
  board_read: 'memory',
  kilo_memory_recall: 'memory',
  kilo_memory_save: 'memory',
  browser_open: 'fetch',
  notebook_edit: 'edit',
  notebook_execute: 'execute',
  notebook_read: 'read',
  open_plan: 'read',
  plan_exit: 'switch_mode',
  repo_overview: 'list',
  semantic_search: 'search',
  // A message to the reader, and a file sent along the same channel. Kilo reaches
  // the paired mobile application with both.
  notify_user: 'message',
  send_file: 'message',
  // `chart` renders a Chart.js configuration, and `generate_image` returns a
  // picture. Both put something for the reader to LOOK at on the row, which no
  // kind above says, and a chart is not the photograph that `image` describes.
  chart: 'chart',
  generate_image: 'image',
  // The root goal worker states its own outcome and touches nothing.
  goal_report: 'report',
}

/**
 * The kind Kilo's own protocol could not state, or undefined to keep the frame's.
 *
 * Undefined rather than `other`, so a caller can tell "this table has no opinion"
 * from "this tool is uncategorized" -- the frame's own kind must win in the first
 * case and would be overwritten in the second.
 */
export function kiloToolKind(toolName: string): ToolKind | undefined {
  // `Object.hasOwn`, not a bare lookup: an agent chooses its own tool names, and a tool
  // called `constructor` or `toString` answers from `Object.prototype` instead of
  // reporting that the table holds no entry.
  return Object.hasOwn(KILO_TOOL_KINDS, toolName) ? KILO_TOOL_KINDS[toolName] : undefined
}
