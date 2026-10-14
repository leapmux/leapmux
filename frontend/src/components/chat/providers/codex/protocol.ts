/** Native code execution content that only the frontend interprets. */
export const CODEX_EXEC_CONTENT = {
  Image: 'input_image',
  ImageURL: 'image_url',
} as const

/** The executor prepends this header before any script output. */
export const CODEX_EXEC_HEADER = {
  Failed: 'Script failed',
} as const
