import { PREFIX_EDITOR_MIN_HEIGHT, safeGetString, safeRemoveItem, safeSetString } from '~/lib/browserStorage'

/** Minimum height (px) of the markdown editor wrapper. */
export const EDITOR_MIN_HEIGHT = 38

/** Build the localStorage key for a per-agent editor min-height override. */
export function editorMinHeightKey(agentId: string): string {
  return `${PREFIX_EDITOR_MIN_HEIGHT}${agentId}`
}

/**
 * Clamp a raw height value into the [EDITOR_MIN_HEIGHT, maxHeight] window,
 * matching the behavior of the resize handle drag handler. `maxHeight` may
 * be a fractional (e.g. 75% of viewport) or absolute pixel value.
 */
export function clampEditorHeight(rawHeight: number, maxHeight: number): number {
  return Math.max(EDITOR_MIN_HEIGHT, Math.min(maxHeight, rawHeight))
}

/**
 * Read a previously-stored per-agent override. Returns `undefined` if no
 * override exists or if the stored value is below the minimum (corrupt /
 * pre-clamp data).
 */
export function getStoredEditorMinHeight(agentId: string): number | undefined {
  const stored = safeGetString(editorMinHeightKey(agentId))
  if (stored) {
    const val = Number.parseInt(stored, 10)
    if (!Number.isNaN(val) && val >= EDITOR_MIN_HEIGHT)
      return val
  }
  return undefined
}

/**
 * Persist a per-agent override after a resize-drag ends. Values strictly
 * greater than the minimum are stored; values equal to or below the minimum
 * (or `undefined`) clear the stored override so the editor returns to its
 * natural single-line default on the next reload.
 */
export function persistEditorMinHeight(agentId: string, value: number | undefined): void {
  const key = editorMinHeightKey(agentId)
  if (value !== undefined && value > EDITOR_MIN_HEIGHT) {
    safeSetString(key, String(value))
  }
  else {
    safeRemoveItem(key)
  }
}

/** Unconditionally clear a per-agent override (used by the double-click reset). */
export function clearEditorMinHeight(agentId: string): void {
  safeRemoveItem(editorMinHeightKey(agentId))
}
