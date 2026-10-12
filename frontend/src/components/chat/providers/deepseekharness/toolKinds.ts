import type { ToolKind } from '../../model/toolKind'
import { DEEPSEEK_HARNESS_TOOL } from '~/generated/contracts/deepseek-harness-protocol'

const DEEPSEEK_HARNESS_TOOL_KINDS: ReadonlyMap<string, ToolKind> = new Map<string, ToolKind>([
  [DEEPSEEK_HARNESS_TOOL.Bash, 'execute'],
  [DEEPSEEK_HARNESS_TOOL.Read, 'read'],
  [DEEPSEEK_HARNESS_TOOL.ReadImage, 'read'],
  [DEEPSEEK_HARNESS_TOOL.Write, 'write'],
  [DEEPSEEK_HARNESS_TOOL.Edit, 'edit'],
  [DEEPSEEK_HARNESS_TOOL.AskUserQuestion, 'question'],
  [DEEPSEEK_HARNESS_TOOL.ExitPlanMode, 'switch_mode'],
  [DEEPSEEK_HARNESS_TOOL.TodoWrite, 'todo'],
  [DEEPSEEK_HARNESS_TOOL.Subagent, 'agent'],
  [DEEPSEEK_HARNESS_TOOL.SubagentFork, 'agent'],
  [DEEPSEEK_HARNESS_TOOL.SendMessage, 'message'],
  [DEEPSEEK_HARNESS_TOOL.ListAgents, 'agents'],
  [DEEPSEEK_HARNESS_TOOL.Workflow, 'execute'],
  [DEEPSEEK_HARNESS_TOOL.RunCode, 'execute'],
  [DEEPSEEK_HARNESS_TOOL.InterruptAgent, 'message'],
])

export function deepseekHarnessToolKind(name: string): ToolKind {
  return DEEPSEEK_HARNESS_TOOL_KINDS.get(name) ?? 'unspecified'
}
