import type { ToolKind } from '../../model/toolKind'
import { KIMI_TOOL } from '~/generated/contracts/kimi-protocol'
import { parseMcpToolName } from '../../model/mcpToolCall'

/**
 * The shared tool kind each Kimi Code tool takes.
 *
 * Kimi Code reports a tool by NAME, so this table is where the name becomes the
 * closed kind that drives the icon, the label and the body. The names are the ones
 * the 2.0.2 server sends the model (probe `tools-main.json`).
 *
 * A Map rather than an object: a Model Context Protocol tool may be called
 * `constructor` or `toString`, and a plain object answers those two names from
 * `Object.prototype` instead of reporting that it holds no entry.
 */
const KIMI_TOOL_KINDS: ReadonlyMap<string, ToolKind> = new Map<string, ToolKind>([
  [KIMI_TOOL.Bash, 'execute'],
  [KIMI_TOOL.Read, 'read'],
  // A media read returns the picture or the clip, which the read card draws.
  [KIMI_TOOL.ReadMediaFile, 'read'],
  [KIMI_TOOL.Write, 'write'],
  [KIMI_TOOL.Edit, 'edit'],
  [KIMI_TOOL.Glob, 'glob'],
  [KIMI_TOOL.Grep, 'grep'],
  [KIMI_TOOL.FetchURL, 'fetch'],
  [KIMI_TOOL.WebSearch, 'web_search'],
  [KIMI_TOOL.TodoList, 'todo'],
  // Both start subagents, which the subagent card and the child transcript draw.
  [KIMI_TOOL.Agent, 'agent'],
  [KIMI_TOOL.AgentSwarm, 'agent'],
  [KIMI_TOOL.AskUserQuestion, 'question'],
  [KIMI_TOOL.Skill, 'skill'],
  // The three calls that act on a background task the session already started.
  [KIMI_TOOL.TaskList, 'task'],
  [KIMI_TOOL.TaskOutput, 'task'],
  [KIMI_TOOL.TaskStop, 'task'],
  [KIMI_TOOL.WaitFor, 'wait'],
  // Both halves of the plan-mode switch.
  [KIMI_TOOL.EnterPlanMode, 'switch_mode'],
  [KIMI_TOOL.ExitPlanMode, 'switch_mode'],
  // A cron entry fires later, whichever half of its lifecycle the call is.
  [KIMI_TOOL.CronCreate, 'trigger'],
  [KIMI_TOOL.CronList, 'trigger'],
  [KIMI_TOOL.CronDelete, 'trigger'],
  // The four goal calls report and change the session's goal and touch no file. The
  // goal card states the goal itself; each call row states what the call reported.
  [KIMI_TOOL.CreateGoal, 'report'],
  [KIMI_TOOL.GetGoal, 'report'],
  [KIMI_TOOL.UpdateGoal, 'report'],
  [KIMI_TOOL.SetGoalBudget, 'report'],
  // A notice the agent sends the user, which is a message rather than a report.
  [KIMI_TOOL.NotifyUser, 'message'],
])

/**
 * The kind of one Kimi Code tool.
 *
 * An `mcp__<server>__<tool>` name is a Model Context Protocol tool. A name the table
 * does not list is one a later release adds, and it takes the uncategorized card.
 * An empty name states no kind.
 */
export function kimiToolKind(toolName: string): ToolKind {
  const own = KIMI_TOOL_KINDS.get(toolName)
  if (own)
    return own
  if (parseMcpToolName(toolName))
    return 'mcp'
  return toolName ? 'other' : 'unspecified'
}
