import { OPENCODE_TOOL_NAMES } from './toolNames'

export type OpenCodeToolKind = 'edit' | 'glob' | 'grep' | 'question' | 'read' | 'execute' | 'skill' | 'agent' | 'fetch' | 'web_search' | 'write' | 'mcp' | 'todo'

/** The native tool name determines the neutral result reader. */
export const OPENCODE_TOOL_KINDS: Readonly<Record<string, OpenCodeToolKind>> = {
  [OPENCODE_TOOL_NAMES.Edit]: 'edit',
  [OPENCODE_TOOL_NAMES.Glob]: 'glob',
  [OPENCODE_TOOL_NAMES.Grep]: 'grep',
  [OPENCODE_TOOL_NAMES.Question]: 'question',
  [OPENCODE_TOOL_NAMES.Read]: 'read',
  [OPENCODE_TOOL_NAMES.Shell]: 'execute',
  [OPENCODE_TOOL_NAMES.Skill]: 'skill',
  [OPENCODE_TOOL_NAMES.Subagent]: 'agent',
  [OPENCODE_TOOL_NAMES.WebFetch]: 'fetch',
  [OPENCODE_TOOL_NAMES.WebSearch]: 'web_search',
  [OPENCODE_TOOL_NAMES.Write]: 'write',
  [OPENCODE_TOOL_NAMES.Execute]: 'execute',
  [OPENCODE_TOOL_NAMES.TodoWrite]: 'todo',
}

export function openCodeToolKind(name: string): OpenCodeToolKind {
  return Object.hasOwn(OPENCODE_TOOL_KINDS, name)
    ? OPENCODE_TOOL_KINDS[name] ?? 'mcp'
    : 'mcp'
}
