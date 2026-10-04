/** Validate neutral file paths and keep their first occurrence in the reported order. */
export function normalizeOutputFilePaths(value: unknown): readonly string[] {
  if (!Array.isArray(value))
    throw new TypeError('Output file paths require an array of strings.')
  const paths: string[] = []
  const seen = new Set<string>()
  for (const path of value) {
    if (typeof path !== 'string' || path.includes('\0'))
      throw new TypeError('An output file path requires text without NUL.')
    if (!path.trim() || seen.has(path))
      continue
    seen.add(path)
    paths.push(path)
  }
  return Object.freeze(paths)
}
