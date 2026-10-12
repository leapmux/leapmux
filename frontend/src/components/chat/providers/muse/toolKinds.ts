import type { ToolKind } from '../../model/toolKind'
import { MUSE_TOOL } from './toolNames'

export const MUSE_TOOL_KINDS: ReadonlyMap<string, ToolKind> = new Map([
  [MUSE_TOOL.Bash, 'execute'],
  [MUSE_TOOL.BashInput, 'execute'],
  [MUSE_TOOL.ReadFile, 'read'],
  [MUSE_TOOL.WriteFile, 'write'],
  [MUSE_TOOL.EditFile, 'edit'],
  [MUSE_TOOL.Search, 'grep'],
  [MUSE_TOOL.WriteTodos, 'todo'],
  [MUSE_TOOL.SubagentSpawn, 'agent'],
  [MUSE_TOOL.SubagentReadResult, 'task'],
  [MUSE_TOOL.SubagentWait, 'task'],
  [MUSE_TOOL.WebFetch, 'fetch'],
  [MUSE_TOOL.WebSearch, 'web_search'],
  [MUSE_TOOL.ReadSkill, 'skill'],
])
