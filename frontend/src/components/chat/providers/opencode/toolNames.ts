/**
 * The registry ids of the family's own tools, which arrive as the call TITLE.
 *
 * The protocol itself carries no tool name, so the title is the only place the
 * registry id appears -- and the shared build cannot read it.
 */
export const OPENCODE_TOOL_NAMES = {
  QUESTION: 'question',
  TASK: 'task',
  TODO_WRITE: 'todowrite',
  BASH: 'bash',
  GLOB: 'glob',
  GREP: 'grep',
} as const
