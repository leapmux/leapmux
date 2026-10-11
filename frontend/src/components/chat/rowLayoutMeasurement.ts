/** Read a row height only when the element supplies usable layout. */
export function rowLayoutMeasurement(element: Element | undefined): number | undefined {
  if (!element?.isConnected)
    return undefined
  const { height, width } = element.getBoundingClientRect()
  if (!Number.isFinite(height) || height < 0)
    return undefined
  if (height > 0)
    return height
  // An empty row needs layout width. A hidden view has no layout width.
  if (!Number.isFinite(width) || width <= 0)
    return undefined
  // An image can give an unready row a zero height before it loads.
  if (Array.from(element.querySelectorAll('img')).some(image => !image.complete))
    return undefined
  return 0
}
