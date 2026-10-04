/** Native fields that only the browser reads. */

/** Native fields for tool result pointers. */
export const CODEBUDDY_RESULT_FIELD = {
  Type: 'type',
  SessionID: 'session_id',
  Message: 'message',
  Content: 'content',
  ToolUseID: 'tool_use_id',
  Text: 'text',
} as const

/** Native fields for tool result pointers. */
export const CODEBUDDY_BLOCK_TYPE = {
  ToolResult: 'tool_result',
  Text: 'text',
} as const
