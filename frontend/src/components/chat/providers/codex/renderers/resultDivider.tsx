import type { ResultDividerModel } from '../../registry'
import { isObject, pickObject, pickString } from '~/lib/jsonPick'
import { CODEX_STATUS } from '~/types/toolMessages'

/**
 * Codex `turn/completed` → result_divider model. A failed turn with an error
 * object renders the message (and `additionalDetails` inline) in danger color;
 * any other status renders a plain `Turn {status}`. Returns null when the turn
 * carries no status; classify only routes a status-bearing turn here, so the
 * null branch is a defensive guard rather than a routine "render nothing" path.
 */
export function codexResultDivider(parsed: unknown): ResultDividerModel | null {
  if (!isObject(parsed))
    return null
  const turn = pickObject(parsed, 'turn')
  const status = pickString(turn, 'status')
  if (!status)
    return null

  const error = pickObject(turn, 'error')
  if (status === CODEX_STATUS.FAILED && error) {
    // `|| 'Unknown error'` (not just pickString's missing-key fallback) so an
    // explicit empty-string message doesn't render a label-less red divider --
    // pickString treats '' as a present string and would otherwise pass it through.
    const message = pickString(error, 'message') || 'Unknown error'
    const details = pickString(error, 'additionalDetails')
    return { label: details ? `${message} — ${details}` : message, isError: true }
  }
  return { label: `Turn ${status}` }
}
