import type { ToolKind } from '../../model/toolKind'
import { OH_MY_PI_TOOL } from '~/generated/contracts/ohmypi-protocol'

/**
 * The shared tool kind each Oh My Pi tool declares.
 *
 * omp reports a tool by NAME alone, so this table is where the name becomes the closed
 * kind that drives the icon, the label, the title and the input summary. A name that
 * the table does not list is an extension tool, a Model Context Protocol tool
 * (`mcp__<server>_<tool>`) or a tool of a later omp, and it keeps the generic card.
 *
 * Several names share a kind, and `ohMyPiReclassify` corrects a kind that the name
 * alone states wrongly: a `read` of a URL is a fetch, and a process operation of `hub`
 * is a command. A `read` of a directory stays a read, because omp prints the directory
 * as an indented tree with a cap for each level, and the tree draws as it is.
 *
 * A Map rather than an object, because an extension can register a tool called
 * `constructor` or `toString`, and a plain object answers those two names from
 * `Object.prototype` instead of reporting that it holds no entry.
 */
const OH_MY_PI_TOOL_KINDS: ReadonlyMap<string, ToolKind> = new Map<string, ToolKind>([
  [OH_MY_PI_TOOL.Read, 'read'],
  [OH_MY_PI_TOOL.Bash, 'execute'],
  // `eval` runs a cell of Python or JavaScript in omp's own kernel: a command, in
  // another language.
  [OH_MY_PI_TOOL.Eval, 'execute'],
  [OH_MY_PI_TOOL.Edit, 'edit'],
  // The name omp gives its edit tool when `edit.mode` is `apply_patch`.
  [OH_MY_PI_TOOL.ApplyPatch, 'edit'],
  [OH_MY_PI_TOOL.Write, 'write'],
  [OH_MY_PI_TOOL.Glob, 'glob'],
  [OH_MY_PI_TOOL.Grep, 'grep'],
  // A semantic search and a syntax-tree search: both query the code the session
  // holds, and neither answers with the line matches `grep` draws.
  [OH_MY_PI_TOOL.Find, 'search'],
  [OH_MY_PI_TOOL.AstGrep, 'search'],
  [OH_MY_PI_TOOL.Task, 'agent'],
  // The coordination tool: messages between agents, and control of background jobs
  // and processes. Each call states an operation and a message or a target. A
  // process operation reclassifies to a command.
  [OH_MY_PI_TOOL.Hub, 'message'],
  [OH_MY_PI_TOOL.Todo, 'todo'],
  [OH_MY_PI_TOOL.WebSearch, 'web_search'],
  [OH_MY_PI_TOOL.Ask, 'question'],
  // What a subagent hands back to its parent.
  [OH_MY_PI_TOOL.Yield, 'report'],
  // The goal tool reports the goal's state after each operation.
  [OH_MY_PI_TOOL.Goal, 'report'],
  [OH_MY_PI_TOOL.Think, 'think'],
])

/**
 * The tool kind one omp tool declares, or the unspecified kind for a name it does not
 * know.
 *
 * The empty answer is what `toolVocabulary.test.ts` reads to find a tool the table
 * forgot, so it stays the literal table lookup. No ROW carries it:
 * `ohMyPiReclassify` gives an unnamed tool the `mcp` kind that matches the card it
 * draws.
 */
export function ohMyPiToolKind(toolName: string): ToolKind {
  return OH_MY_PI_TOOL_KINDS.get(toolName) ?? 'unspecified'
}
