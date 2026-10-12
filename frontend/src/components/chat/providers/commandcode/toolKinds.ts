import type { ToolKind } from '../../model/toolKind'
import { COMMAND_CODE_TOOL } from '~/generated/contracts/commandcode-protocol'
import { parseMcpToolName } from '../../model/mcpToolCall'
import { COMMAND_CODE_TOOL_NAME } from './protocol'

const COMMAND_CODE_TOOL_KINDS: ReadonlyMap<string, ToolKind> = new Map([
  [COMMAND_CODE_TOOL.ShellCommand, 'execute'],
  [COMMAND_CODE_TOOL_NAME.PowerShell, 'execute'],
  [COMMAND_CODE_TOOL.ShellOutput, 'task'],
  [COMMAND_CODE_TOOL.KillShell, 'task'],
  [COMMAND_CODE_TOOL.MonitorCommand, 'execute'],
  [COMMAND_CODE_TOOL.Agent, 'agent'],
  [COMMAND_CODE_TOOL.AgentOutput, 'task'],
  [COMMAND_CODE_TOOL.TodoWrite, 'todo'],
  [COMMAND_CODE_TOOL.TaskCreate, 'todo'],
  [COMMAND_CODE_TOOL.TaskUpdate, 'todo'],
  [COMMAND_CODE_TOOL_NAME.ReadFile, 'read'],
  [COMMAND_CODE_TOOL_NAME.WriteFile, 'write'],
  [COMMAND_CODE_TOOL_NAME.EditFile, 'edit'],
  [COMMAND_CODE_TOOL_NAME.ReadDirectory, 'list'],
  [COMMAND_CODE_TOOL_NAME.Glob, 'glob'],
  [COMMAND_CODE_TOOL_NAME.Grep, 'grep'],
  [COMMAND_CODE_TOOL_NAME.ActivateSkill, 'skill'],
  [COMMAND_CODE_TOOL_NAME.Sleep, 'wait'],
  [COMMAND_CODE_TOOL_NAME.WebSearch, 'web_search'],
  [COMMAND_CODE_TOOL_NAME.WebFetch, 'fetch'],
  [COMMAND_CODE_TOOL_NAME.SearchTools, 'other'],
  [COMMAND_CODE_TOOL_NAME.TaskList, 'todo'],
  [COMMAND_CODE_TOOL_NAME.TaskGet, 'todo'],
  [COMMAND_CODE_TOOL_NAME.ShellTasks, 'task'],
  [COMMAND_CODE_TOOL_NAME.RunCommand, 'other'],
])

export function commandCodeToolKind(name: string): ToolKind {
  return COMMAND_CODE_TOOL_KINDS.get(name) ?? (parseMcpToolName(name) ? 'mcp' : 'other')
}
