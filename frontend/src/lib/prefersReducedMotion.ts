// Live "prefers reduced motion" read, shared by the UIs that gate JS-driven
// motion on it (the thinking-token odometer's imperative digit roll, the
// directory-tree expand scroll). `MediaQueryList.matches` is always current, so
// callers read it fresh on demand — toggling the OS setting takes effect on the
// next read with no listener to register, leak, or fall out of sync. The query
// handle is resolved once at module load and guarded for SSR / jsdom (no
// `window`, no `matchMedia`), where it reports false (motion allowed).
const reducedMotionQuery
  = typeof window !== 'undefined' && typeof window.matchMedia === 'function'
    ? window.matchMedia('(prefers-reduced-motion: reduce)')
    : undefined

export function prefersReducedMotion(): boolean {
  return reducedMotionQuery?.matches ?? false
}
