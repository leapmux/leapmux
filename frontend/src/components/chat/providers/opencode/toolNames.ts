import { OPENCODE_NATIVE_TOOL } from '~/generated/contracts/opencode-native-protocol'

/** Native tool IDs offered by OpenCode v2. */
export const OPENCODE_TOOL_NAMES = {
  Edit: 'edit',
  Glob: 'glob',
  Grep: 'grep',
  Question: 'question',
  Read: 'read',
  Shell: OPENCODE_NATIVE_TOOL.Shell,
  Skill: 'skill',
  Subagent: OPENCODE_NATIVE_TOOL.Subagent,
  WebFetch: 'webfetch',
  WebSearch: 'websearch',
  Write: 'write',
  Execute: 'execute',
  TodoWrite: OPENCODE_NATIVE_TOOL.TodoWrite,
} as const
