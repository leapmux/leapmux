import type { JSX } from 'solid-js'
import { isObject, pickNumber, pickObject, pickString } from '~/lib/jsonPick'
import { controlResponseMessage } from '../../../messageStyles.css'
import { PI_EVENT, PI_EXTENSION_METHOD } from '../protocol'

const COMPACTION_REASON_LABELS: Record<string, string> = {
  manual: 'Manually compacted context',
  threshold: 'Compacted context after threshold',
  overflow: 'Compacted context after overflow',
}

/**
 * Compute a human-readable line for a Pi notification event. Returns null for
 * shapes we don't recognize so the plugin can fall back to the shared
 * provider-neutral notification renderer (settings_changed, interrupted, ...).
 */
export function describePiNotification(parsed: unknown): string | null {
  if (!isObject(parsed))
    return null
  const parent = parsed as Record<string, unknown>
  const type = pickString(parent, 'type')

  if (type === PI_EVENT.CompactionStart) {
    const reason = pickString(parent, 'reason')
    if (reason && COMPACTION_REASON_LABELS[reason])
      return `${COMPACTION_REASON_LABELS[reason]}…`
    return 'Compacting context…'
  }

  if (type === PI_EVENT.CompactionEnd) {
    if (parent.aborted === true)
      return 'Context compaction aborted'
    const reason = pickString(parent, 'reason')
    const tokensBefore = pickNumber(pickObject(parent, 'result'), 'tokensBefore')
    const reasonLabel = COMPACTION_REASON_LABELS[reason] ?? 'Compacted context'
    return tokensBefore ? `${reasonLabel} (was ${tokensBefore.toLocaleString()} tokens)` : reasonLabel
  }

  if (type === PI_EVENT.AutoRetryStart) {
    const attempt = pickNumber(parent, 'attempt')
    const max = pickNumber(parent, 'maxAttempts')
    const delay = pickNumber(parent, 'delayMs')
    const err = pickString(parent, 'errorMessage')
    const head = `Auto-retry ${attempt ?? '?'}/${max ?? '?'}`
    const tail = delay != null ? ` in ${Math.round(delay / 1000)}s` : ''
    return err ? `${head}${tail} — ${err}` : `${head}${tail}…`
  }

  if (type === PI_EVENT.AutoRetryEnd) {
    if (parent.success === true) {
      const attempt = pickNumber(parent, 'attempt')
      return `Auto-retry succeeded (attempt ${attempt ?? '?'})`
    }
    const finalErr = pickString(parent, 'finalError')
    return finalErr ? `Auto-retry failed: ${finalErr}` : 'Auto-retry failed'
  }

  if (type === PI_EVENT.ExtensionError) {
    const ext = pickString(parent, 'extensionPath')
    const evt = pickString(parent, 'event')
    const err = pickString(parent, 'error')
    return `Extension error${ext ? ` in ${ext}` : ''}${evt ? ` (${evt})` : ''}${err ? `: ${err}` : ''}`
  }

  // Pi `extension_ui_request` with `method:"notify"` carries a
  // user-visible message; surface it directly. Other methods get a
  // method-name label so the user still sees that an extension fired
  // something.
  if (type === PI_EVENT.ExtensionUIRequest) {
    const method = pickString(parent, 'method')
    if (method === PI_EXTENSION_METHOD.Notify)
      return pickString(parent, 'message') || null
    return method ? `Extension UI: ${method}` : 'Extension UI request'
  }

  return null
}

/**
 * Pi notification renderer. Plain function (not a Solid component) so the
 * plugin can fall through to the shared notification rendering when this
 * returns null. The capitalize-aware solid/components-return-once rule does
 * not flag lowercase factory functions.
 */
export function piNotificationRenderer(parsed: unknown): JSX.Element | null {
  const text = describePiNotification(parsed)
  if (text === null)
    return null
  return <div class={controlResponseMessage}>{text}</div>
}
