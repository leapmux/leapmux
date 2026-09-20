import type { ToolKind } from '../../model/toolKind'
import { ZCODE_TOOL } from '~/generated/contracts/zcode-protocol'

/**
 * The shared tool kind each ZCode tool declares.
 *
 * ZCode reports a tool by NAME alone, so this table is where the name becomes the
 * closed kind that drives the icon, the label, the title and the input summary. A
 * name that is absent from the table keeps the generic row, which states the name
 * and repeats the arguments -- the same answer the Agent Client Protocol gives for
 * its own `other` kind. Every tool `ZCODE_TOOL` names reaches a kind, so only a
 * tool from a later release can take it; `toolVocabulary.test.ts` is the guard.
 *
 * A Map rather than an object: a Model Context Protocol tool may be called
 * `constructor` or `toString`, and a plain object answers those two names from
 * `Object.prototype` instead of reporting that it holds no entry.
 */
const ZCODE_TOOL_KINDS: ReadonlyMap<string, ToolKind> = new Map<string, ToolKind>([
  [ZCODE_TOOL.Bash, 'execute'],
  [ZCODE_TOOL.Read, 'read'],
  [ZCODE_TOOL.Write, 'write'],
  [ZCODE_TOOL.Edit, 'edit'],
  [ZCODE_TOOL.Glob, 'glob'],
  [ZCODE_TOOL.Grep, 'grep'],
  [ZCODE_TOOL.Agent, 'agent'],
  [ZCODE_TOOL.Task, 'agent'],
  [ZCODE_TOOL.TodoWrite, 'todo'],
  [ZCODE_TOOL.TodoRead, 'todo'],
  [ZCODE_TOOL.WebFetch, 'fetch'],
  [ZCODE_TOOL.WebSearch, 'web_search'],
  [ZCODE_TOOL.ServerWebSearch, 'web_search'],
  [ZCODE_TOOL.ApplyPatch, 'edit'],
  [ZCODE_TOOL.AskUserQuestion, 'question'],
  [ZCODE_TOOL.GoalRead, 'read'],
  [ZCODE_TOOL.ReadSessionContext, 'read'],
  // The three JavaScript tools run code in ZCode's own sandbox, which is the same
  // work a shell runs: `js` evaluates a snippet, and the other two manage the
  // sandbox that evaluates it.
  [ZCODE_TOOL.Js, 'execute'],
  [ZCODE_TOOL.JsAddNodeModuleDir, 'execute'],
  [ZCODE_TOOL.JsReset, 'execute'],
  // Both halves of the plan-mode switch, which is the change the Agent Client
  // Protocol spells. `ExitPlanMode` still leaves the tool path when it carries a
  // plan -- see `zcodeExtractRow` -- and this kind serves the row that does not.
  [ZCODE_TOOL.EnterPlanMode, 'switch_mode'],
  [ZCODE_TOOL.ExitPlanMode, 'switch_mode'],
  // Reading a background task's output and stopping it both act on a task the
  // transcript already opened, rather than on the workspace.
  [ZCODE_TOOL.TaskOutput, 'task'],
  [ZCODE_TOOL.TaskStop, 'task'],
  // A message to a peer, and the reply half of the same exchange.
  [ZCODE_TOOL.SendMessage, 'message'],
  [ZCODE_TOOL.RespondToCoordinator, 'message'],
  // A cron entry fires later, whichever half of its lifecycle the call is.
  [ZCODE_TOOL.CronCreate, 'trigger'],
  [ZCODE_TOOL.CronList, 'trigger'],
  [ZCODE_TOOL.CronUpdate, 'trigger'],
  [ZCODE_TOOL.CronDelete, 'trigger'],
  [ZCODE_TOOL.Skill, 'skill'],
])

/** The kind of one ZCode tool. An empty name states no kind; an unknown one is uncategorized. */
export function zcodeToolKind(toolName: string): ToolKind {
  return ZCODE_TOOL_KINDS.get(toolName) ?? (toolName ? 'other' : 'unspecified')
}
