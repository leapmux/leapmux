import type { JSX } from 'solid-js'
import { isObject } from '~/lib/jsonPick'
import { resultDivider } from '../../../messageStyles.css'

/** Render an ACP result divider (turn completion). */
export function acpResultDividerRenderer(parsed: unknown): JSX.Element | null {
  if (!isObject(parsed))
    return null
  const obj = parsed as Record<string, unknown>
  const reason = obj.stopReason as string | undefined
  const label = reason && reason !== 'end_turn' ? `Turn ended (${reason})` : 'Turn ended'
  return <div class={resultDivider}>{label}</div>
}
