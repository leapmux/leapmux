import { sameValueZero } from './shallowEqual'

export interface RovingDestination<T> {
  value: T
}

type RovingKeyEvent = Pick<KeyboardEvent, 'altKey' | 'ctrlKey' | 'key' | 'metaKey'>

/** Find the destination for one radio or tab navigation key. */
export function nextRovingValue<T>(
  values: readonly T[],
  current: T,
  event: RovingKeyEvent,
): RovingDestination<T> | undefined {
  if (event.altKey || event.ctrlKey || event.metaKey || values.length === 0)
    return undefined

  const found = values.findIndex(value => sameValueZero(value, current))
  const index = found < 0 ? 0 : found
  let destination: number
  switch (event.key) {
    case 'ArrowRight':
    case 'ArrowDown':
      destination = (index + 1) % values.length
      break
    case 'ArrowLeft':
    case 'ArrowUp':
      destination = (index - 1 + values.length) % values.length
      break
    case 'Home':
      destination = 0
      break
    case 'End':
      destination = values.length - 1
      break
    default:
      return undefined
  }
  return { value: values[destination]! }
}
