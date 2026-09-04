import { onCleanup } from 'solid-js'

export interface KeyedElementRefs<K, E extends Element> {
  get: (key: K) => E | undefined
  register: (key: K, element: E) => void
}

/** Keep one live element for each key and remove only the element that leaves. */
export function createKeyedElementRefs<K, E extends Element>(): KeyedElementRefs<K, E> {
  const elements = new Map<K, E>()
  return {
    get: key => elements.get(key),
    register: (key, element) => {
      elements.set(key, element)
      onCleanup(() => {
        if (elements.get(key) === element)
          elements.delete(key)
      })
    },
  }
}
