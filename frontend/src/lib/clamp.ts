/**
 * Clamp `v` into the closed interval `[lo, hi]`. Assumes `lo <= hi`.
 *
 * The single home for the bare numeric clamp the chat virtualizer's offset engine
 * (`useChatVirtualizer`) and the scroll-anchor math (`chatScrollAnchor`) both need,
 * so the two can't drift on edge behavior (`Math.min(Math.max(...))` vs a ternary
 * ladder). A leaf with no imports.
 */
export function clamp(v: number, lo: number, hi: number): number {
  return v < lo ? lo : v > hi ? hi : v
}
