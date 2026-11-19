/** Native failures that state an explicit user refusal. */
export const OPENCODE_DECLINED_TOOL_ERRORS: ReadonlySet<string> = new Set([
  'The user declined this tool call',
  'The user dismissed this question',
])
