import { createSignal, onCleanup, onMount } from 'solid-js'

export type TileSizeClass = 'full' | 'compact' | 'minimal' | 'micro'
export type TileHeightClass = 'tall' | 'short' | 'tiny'

function widthToSizeClass(width: number): TileSizeClass {
  if (width >= 360)
    return 'full'
  if (width >= 240)
    return 'compact'
  if (width >= 140)
    return 'minimal'
  return 'micro'
}

function heightToHeightClass(height: number): TileHeightClass {
  if (height >= 120)
    return 'tall'
  if (height >= 72)
    return 'short'
  return 'tiny'
}

export function useTileSize(ref: () => HTMLElement | undefined) {
  // Default to large values so the tile starts in 'full'/'tall' mode
  // until the first ResizeObserver measurement arrives.
  const [width, setWidth] = createSignal(Infinity)
  const [height, setHeight] = createSignal(Infinity)

  const sizeClass = (): TileSizeClass => widthToSizeClass(width())
  const heightClass = (): TileHeightClass => heightToHeightClass(height())

  onMount(() => {
    const el = ref()
    if (!el)
      return

    const observer = new ResizeObserver((entries) => {
      for (const entry of entries) {
        setWidth(entry.contentRect.width)
        setHeight(entry.contentRect.height)
      }
    })
    observer.observe(el)
    onCleanup(() => observer.disconnect())
  })

  return { width, height, sizeClass, heightClass }
}
