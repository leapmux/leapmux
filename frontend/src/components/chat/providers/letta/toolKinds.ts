import type { ToolKind } from '../../model/toolKind'
import { LETTA_TOOL } from '~/generated/contracts/letta-protocol'

/**
 * The shared tool kind each Letta Code tool declares.
 *
 * Letta reports a tool by NAME alone, so this table is where the name becomes
 * the closed kind. A Map rather than an object, because a server can register a
 * tool called `constructor` or `toString`.
 */
const LETTA_TOOL_KINDS: ReadonlyMap<string, ToolKind> = new Map<string, ToolKind>([
  [LETTA_TOOL.Bash, 'execute'],
  [LETTA_TOOL.Agent, 'agent'],
  [LETTA_TOOL.AskUserQuestion, 'question'],
  [LETTA_TOOL.Read, 'read'],
  [LETTA_TOOL.Edit, 'edit'],
  [LETTA_TOOL.Write, 'write'],
  [LETTA_TOOL.TaskCreate, 'todo'],
  [LETTA_TOOL.TaskGet, 'todo'],
  [LETTA_TOOL.TaskList, 'todo'],
  [LETTA_TOOL.TaskUpdate, 'todo'],
  [LETTA_TOOL.TaskStop, 'task'],
  [LETTA_TOOL.Monitor, 'trigger'],
  [LETTA_TOOL.Wake, 'trigger'],
  [LETTA_TOOL.Workflow, 'task'],
  [LETTA_TOOL.Skill, 'skill'],
  [LETTA_TOOL.UpdatePlan, 'todo'],
  [LETTA_TOOL.SetWorkingDirectory, 'other'],
  [LETTA_TOOL.EnterWorktree, 'other'],
  [LETTA_TOOL.ExitWorktree, 'other'],
  [LETTA_TOOL.SendAgentMessage, 'message'],
])

/** The tool kind one Letta tool declares, or the unspecified kind. */
export function lettaToolKind(toolName: string): ToolKind {
  return LETTA_TOOL_KINDS.get(toolName) ?? 'unspecified'
}
