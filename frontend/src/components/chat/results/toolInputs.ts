// Providers use these field aliases. Extractors check them in order.
export const TOOL_FILE_PATH_KEYS = ['filePath', 'path', 'file_path'] as const
export const TOOL_OLD_TEXT_KEYS = ['oldText', 'oldString', 'old_string'] as const
export const TOOL_NEW_TEXT_KEYS = ['newText', 'newString', 'new_string'] as const

/** Search targets can be one path or a native array of paths. */
export function toolInputPaths(input: Record<string, unknown>): string[] {
  for (const key of [...TOOL_FILE_PATH_KEYS, 'paths']) {
    const value = input[key]
    const paths = Array.isArray(value) ? value.filter((entry): entry is string => typeof entry === 'string' && entry !== '') : typeof value === 'string' && value !== '' ? [value] : []
    if (paths.length > 0)
      return paths
  }
  return []
}
