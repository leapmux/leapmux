import { createSignal, onCleanup, onMount } from 'solid-js'

export type TileSizeClass = 'full' | 'narrow' | 'compact' | 'minimal' | 'micro'
export type TileHeightClass = 'tall' | 'short' | 'tiny'

export function widthToSizeClass(width: number): TileSizeClass {
  if (width >= 480)
    return 'full'
  if (width >= 360)
    return 'narrow'
  if (width >= 240)
    return 'compact'
  if (width >= 172)
    return 'minimal'
  return 'micro'
}

export function heightToHeightClass(height: number): TileHeightClass {
  if (height >= 120)
    return 'tall'
  if (height >= 72)
    return 'short'
  return 'tiny'
}

// Single ResizeObserver shared across every `useTileSize` consumer. With a
// 5×5 grid that's one browser observer instead of 25 — the per-instance
// allocation, JS↔native callback hop, and observer-loop bookkeeping all
// scale with the count, while a singleton with a Map dispatch costs the
// same as a single observer regardless of how many tiles subscribe.

type Subscriber = (width: number, height: number) => void

const subscribers = new Map<Element, Subscriber>()
let sharedObserver: ResizeObserver | null = null

function getSharedObserver(): ResizeObserver | null {
  if (typeof ResizeObserver === 'undefined')
    return null
  if (sharedObserver === null) {
    sharedObserver = new ResizeObserver((entries) => {
      for (const entry of entries) {
        const cb = subscribers.get(entry.target)
        if (cb)
          cb(entry.contentRect.width, entry.contentRect.height)
      }
    })
  }
  return sharedObserver
}

function subscribeResize(el: Element, cb: Subscriber): () => void {
  const observer = getSharedObserver()
  // Multiple useTileSize hooks on the same element would overwrite — the
  // singleton dispatches to one subscriber per element. Tile is the only
  // consumer today and uses one hook per element, so this is fine.
  subscribers.set(el, cb)
  observer?.observe(el)
  return () => {
    subscribers.delete(el)
    observer?.unobserve(el)
  }
}

export function useTileSize(ref: () => HTMLElement | undefined) {
  // Default to the largest bucket so the tile starts in 'full'/'tall' mode
  // until the first ResizeObserver measurement arrives. Storing buckets
  // (rather than raw pixels) means single-pixel resizes don't re-notify
  // downstream readers.
  const [sizeClass, setSizeClass] = createSignal<TileSizeClass>('full')
  const [heightClass, setHeightClass] = createSignal<TileHeightClass>('tall')

  onMount(() => {
    const el = ref()
    if (!el)
      return
    const unobserve = subscribeResize(el, (width, height) => {
      setSizeClass(widthToSizeClass(width))
      setHeightClass(heightToHeightClass(height))
    })
    onCleanup(unobserve)
  })

  return { sizeClass, heightClass }
}
