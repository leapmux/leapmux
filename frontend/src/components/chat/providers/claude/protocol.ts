/** Native fields that only the browser reads. */

/** Native fields for tool result pointers. */
export const CLAUDE_RESULT_FIELD = {
  Type: 'type',
  SessionID: 'session_id',
  Message: 'message',
  Content: 'content',
  ToolUseID: 'tool_use_id',
  Text: 'text',
  ToolUseResult: 'tool_use_result',
  PersistedOutputPath: 'persistedOutputPath',
  PersistedOutputSize: 'persistedOutputSize',
  IsImage: 'isImage',
} as const

/** Native fields for tool result pointers. */
export const CLAUDE_BLOCK_TYPE = {
  ToolResult: 'tool_result',
  Text: 'text',
} as const
