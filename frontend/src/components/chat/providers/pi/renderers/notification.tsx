import type { NotificationThreadEntry } from '../../registry'
import { isObject, pickNumber, pickObject, pickString } from '~/lib/jsonPick'
import { compactedLabel, COMPACTING_LABEL } from '../../../notificationRenderers'
import { PI_EVENT, PI_EXTENSION_METHOD } from '../protocol'

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

  // Compaction shares the provider-neutral label format ("Context compacted
  // (reason, pre)") so Pi reads the same as Claude/Codex. Pi's `compaction_end`
  // carries only a pre-compaction size (`result.tokensBefore`) and no post count,
  // so the transition degrades to pre-only, exactly as the shared formatter does
  // when post is unknown. The `reason` (manual/threshold/overflow) is the trigger.
  if (type === PI_EVENT.CompactionStart)
    return COMPACTING_LABEL

  if (type === PI_EVENT.CompactionEnd) {
    if (parent.aborted === true)
      return 'Context compaction aborted'
    const reason = pickString(parent, 'reason')
    const tokensBefore = pickNumber(pickObject(parent, 'result'), 'tokensBefore')
    return compactedLabel({
      trigger: reason || undefined,
      pre: tokensBefore ?? undefined,
    })
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
 * Which compaction divider a Pi message maps to, or null when it is not a
 * (renderable) compaction boundary. `compaction_start` is the in-progress
 * spinner ('loading'); a completed `compaction_end` is the boundary; an aborted
 * `compaction_end` produced no boundary, so it stays a plain line (null).
 */
function piCompactionKind(parent: Record<string, unknown>): 'loading' | 'boundary' | null {
  const type = pickString(parent, 'type')
  if (type === PI_EVENT.CompactionStart)
    return 'loading'
  if (type === PI_EVENT.CompactionEnd && parent.aborted !== true)
    return 'boundary'
  return null
}

/**
 * Convert one Pi notification message into thread entries for the shared
 * `renderNotificationThread` (consulted via the plugin's `notificationThreadEntry`,
 * the sole Pi notification render path for both single and consolidated
 * messages). Compaction boundaries become divider entries (icon + label) so Pi
 * matches Claude/Codex visually; everything else is a plain text entry. Without
 * this, a multi-event Pi thread (e.g. two `compaction_end`s, or `auto_retry` +
 * `compaction_end`) would render only its first message. Returns null for shapes
 * Pi does not own so the shared switch can try them.
 */
export function piNotificationThreadEntry(msg: Record<string, unknown>): NotificationThreadEntry[] | null {
  const text = describePiNotification(msg)
  if (text === null)
    return null
  const kind = piCompactionKind(msg)
  if (kind !== null)
    return [{ kind: 'divider', text, loading: kind === 'loading' }]
  return [{ kind: 'text', text }]
}
