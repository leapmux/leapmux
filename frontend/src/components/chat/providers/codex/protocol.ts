/** Native code execution content that only the frontend interprets. */
export const CODEX_EXEC_CONTENT = {
  Text: 'input_text',
  Image: 'input_image',
  ImageURL: 'image_url',
} as const

/** The executor prepends this header before any script output. */
export const CODEX_EXEC_HEADER = {
  Completed: 'Script completed',
  Failed: 'Script failed',
} as const
