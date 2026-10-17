import { CODEX_RAW_TOOL } from '~/generated/contracts/codex-protocol'

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

/** Native cancellation uses a plain string. The caller also checks the match length to exclude a final newline. */
export const CODEX_EXEC_CANCELLATION = /^aborted by user after (\d+\.\d)s$/

/** Codex serializes this dispatch failure as text and omits its internal success flag. */
export const CODEX_EXEC_DISPATCH_FAILURE = `unsupported custom tool call: ${CODEX_RAW_TOOL.Exec}`

/** Read-state metadata does not contain transcript content. */
export const CODEX_READ_STATE_CHANGED_METHOD = 'thread/readState/changed'
