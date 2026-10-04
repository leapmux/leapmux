/**
 * The argument keys each file-path-like input answers to, across providers.
 *
 * This module is the one shared place allowed to hold WIRE tokens: `model/` cannot
 * (its layering guard forbids a provider vocabulary inside the model), and every
 * extractor that lifts a path out of raw arguments needs the same alias list.
 */
export const TOOL_FILE_PATH_KEYS = ['filePath', 'path', 'file_path'] as const
// Junie's `search_replace` states the two sides of a substitution as
// `search`/`replace`; the three spellings below are the other agents'.
export const TOOL_OLD_TEXT_KEYS = ['oldText', 'oldString', 'old_string', 'search'] as const
export const TOOL_NEW_TEXT_KEYS = ['newText', 'newString', 'new_string', 'replace'] as const
// A move states two paths and neither is a `filePath`. Reasonix's `move_file`
// sends `source_path`/`destination_path`; the camelCase and old/new spellings
// are here for the same reason the lists above carry three spellings each.
export const TOOL_SOURCE_PATH_KEYS = ['sourcePath', 'source_path', 'oldPath', 'old_path'] as const
export const TOOL_DESTINATION_PATH_KEYS = ['destinationPath', 'destination_path', 'newPath', 'new_path'] as const

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
