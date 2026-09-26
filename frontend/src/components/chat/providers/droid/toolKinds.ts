import type { ToolKind } from '../../model/toolKind'
import { DROID_TOOL } from '~/generated/contracts/droid-protocol'

/**
 * The shared tool kind each Factory Droid tool declares.
 *
 * Droid reports a tool by NAME alone, so this table is where the name becomes the
 * closed kind that drives the icon, the label, the title and the input summary. A
 * name that the table does not list keeps the generic card.
 *
 * A Map rather than an object, because a server can register a tool called
 * `constructor` or `toString`.
 */
const DROID_TOOL_KINDS: ReadonlyMap<string, ToolKind> = new Map<string, ToolKind>([
  [DROID_TOOL.Execute, 'execute'],
  [DROID_TOOL.Task, 'agent'],
  [DROID_TOOL.AskUser, 'question'],
  [DROID_TOOL.Read, 'read'],
  [DROID_TOOL.LS, 'read'],
  [DROID_TOOL.Grep, 'grep'],
  [DROID_TOOL.Glob, 'glob'],
  [DROID_TOOL.Edit, 'edit'],
  [DROID_TOOL.ApplyPatch, 'edit'],
  [DROID_TOOL.Create, 'edit'],
  [DROID_TOOL.WebSearch, 'web_search'],
  [DROID_TOOL.FetchUrl, 'fetch'],
  [DROID_TOOL.TodoWrite, 'todo'],
  [DROID_TOOL.Skill, 'skill'],
  [DROID_TOOL.ToolSearch, 'search'],
  [DROID_TOOL.ExitSpecMode, 'switch_mode'],
  [DROID_TOOL.GenerateImage, 'image'],
])

/**
 * The tool kind one Droid tool declares, or the unspecified kind for a name it
 * does not know.
 */
export function droidToolKind(toolName: string): ToolKind {
  return DROID_TOOL_KINDS.get(toolName) ?? 'unspecified'
}
