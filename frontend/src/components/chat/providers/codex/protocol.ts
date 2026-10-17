/** Native code execution content that only the frontend interprets. */
export const CODEX_EXEC_CONTENT = {
  Image: 'input_image',
  ImageURL: 'image_url',
} as const

/** The executor prepends one of these headers before any script output. */
export const CODEX_EXEC_HEADER = {
  Failed: 'Script failed',
  Terminated: 'Script terminated',
} as const

/** Read-state metadata does not contain transcript content. */
export const CODEX_READ_STATE_CHANGED_METHOD = 'thread/readState/changed'
