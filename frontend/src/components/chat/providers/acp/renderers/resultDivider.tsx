import type { ResultDividerModel } from '../../registry'
import { isObject, pickString } from '~/lib/jsonPick'

/** ACP result_divider model (turn completion). Null for a non-object message. */
export function acpResultDivider(parsed: unknown): ResultDividerModel | null {
  if (!isObject(parsed))
    return null
  // pickString (not a raw cast) so a non-string stopReason degrades to '' rather
  // than coercing a number/object into the label; matches the other hooks.
  const reason = pickString(parsed, 'stopReason')
  return { label: reason && reason !== 'end_turn' ? `Turn ended (${reason})` : 'Turn ended' }
}
