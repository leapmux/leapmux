// Shared window.matchMedia stub for unit tests. jsdom does not implement
// matchMedia, and xterm's CoreBrowserService watches the device-pixel-ratio
// query through the LEGACY addListener/removeListener pair, so a stub with
// only addEventListener throws inside open(). This stub reports no query as
// matching (always light), records every change handler per query for tests
// that flip a media query, and restores the original matchMedia on cleanup.

type ChangeHandler = (e: { matches: boolean }) => void

export function stubMatchMedia() {
  const original = window.matchMedia
  const handlers = new Map<string, Set<ChangeHandler>>()

  window.matchMedia = ((query: string) => {
    let set = handlers.get(query)
    if (!set) {
      set = new Set()
      handlers.set(query, set)
    }
    const listenerSet = set
    return {
      matches: false,
      addListener: (cb: ChangeHandler) => listenerSet.add(cb),
      removeListener: (cb: ChangeHandler) => listenerSet.delete(cb),
      addEventListener: (_type: string, cb: ChangeHandler) => listenerSet.add(cb),
      removeEventListener: (_type: string, cb: ChangeHandler) => listenerSet.delete(cb),
    }
  }) as unknown as typeof window.matchMedia

  return {
    /** The change handlers registered for one query, in registration order. */
    handlersFor: (query: string): ChangeHandler[] => [...(handlers.get(query) ?? [])],
    restore: () => {
      window.matchMedia = original
    },
  }
}
