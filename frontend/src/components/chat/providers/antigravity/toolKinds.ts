import type { ToolKind } from '../../model/toolKind'
import { ANTIGRAVITY_TOOL } from '~/generated/contracts/antigravity-protocol'

export const ANTIGRAVITY_TOOL_KINDS: ReadonlyMap<string, ToolKind> = new Map<string, ToolKind>([
  [ANTIGRAVITY_TOOL.ReadFile, 'read'],
  [ANTIGRAVITY_TOOL.ViewFile, 'read'],
  [ANTIGRAVITY_TOOL.WriteFile, 'write'],
  [ANTIGRAVITY_TOOL.EditFile, 'edit'],
  [ANTIGRAVITY_TOOL.RunCommand, 'execute'],
  [ANTIGRAVITY_TOOL.SearchWeb, 'web_search'],
  ['read_url_content', 'fetch'],
  ['grep_search', 'grep'],
  ['find_by_name', 'glob'],
  ['list_dir', 'list'],
  [ANTIGRAVITY_TOOL.InvokeSubagent, 'agent'],
  [ANTIGRAVITY_TOOL.ManageSubagents, 'agents'],
  [ANTIGRAVITY_TOOL.ManageTask, 'task'],
  [ANTIGRAVITY_TOOL.SendMessage, 'message'],
  [ANTIGRAVITY_TOOL.Schedule, 'trigger'],
  [ANTIGRAVITY_TOOL.AskQuestion, 'question'],
  ['generate_image', 'image'],
  [ANTIGRAVITY_TOOL.CallMcpTool, 'mcp'],
  [ANTIGRAVITY_TOOL.ListResources, 'mcp'],
  [ANTIGRAVITY_TOOL.ReadResource, 'mcp'],
  [ANTIGRAVITY_TOOL.DefineSubagent, 'agents'],
])

export function antigravityToolKind(toolName: string): ToolKind {
  return ANTIGRAVITY_TOOL_KINDS.get(toolName) ?? (toolName ? 'other' : 'unspecified')
}
