import type { ListResult } from '../../../model/tools/list'
import { isObject, pickBool, pickNumber, pickString, stringArray } from '~/lib/jsonPick'

/**
 * The listing one call answered with, or null for a text that is not one.
 *
 * Two tools answer with a listing:
 *
 *   - `list_dir` answers `[{name, is_dir}]`, or `{entries, total_entries, truncated}`
 *     for a directory the tool cut at its cap. A directory states itself with a
 *     trailing `/`, which is how the list body tells the two apart without a column of
 *     its own.
 *   - `project_map` answers `{tree, summary, key_files}`. The key files are the
 *     entries, and the summary is the notice above them. The ASCII tree repeats the
 *     same paths with indentation that the list body has no column for, so the row
 *     leaves it out.
 */
export function codewhaleListResult(text: string): ListResult | null {
  let document: unknown
  try {
    document = JSON.parse(text)
  }
  catch {
    return null
  }
  if (isObject(document) && Array.isArray(document.key_files)) {
    const summary = pickString(document, 'summary').trim()
    return {
      entries: stringArray(document.key_files).filter(Boolean).map(path => ({ path })),
      ...(summary ? { notice: summary } : {}),
    }
  }
  const capped = isObject(document) && Array.isArray(document.entries) ? document : null
  const raw = capped ? capped.entries : document
  if (!Array.isArray(raw))
    return null
  const entries = raw.filter(isObject).flatMap((entry) => {
    const name = pickString(entry, 'name')
    return name ? [{ path: entry.is_dir === true ? `${name}/` : name }] : []
  })
  const total = pickNumber(capped, 'total_entries')
  return {
    entries,
    ...(total !== null ? { totalEntries: total } : {}),
    ...(pickBool(capped, 'truncated') ? { truncated: true } : {}),
  }
}
