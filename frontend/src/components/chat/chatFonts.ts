import type { Accessor } from 'solid-js'
import { batch, createComputed, createMemo, createSignal, onCleanup, untrack } from 'solid-js'
import { FONT_LOAD_MAX_RETRIES, FONT_LOAD_RETRY_BASE_MS, fontVariantAvailable, fontVariantSpecs, tryLoadFontVariant } from '~/lib/fontFaces'

/**
 * The longest time that staging waits for the chat fonts. After it, the rows measure
 * with the fonts that the browser has, and measure again when a font arrives late.
 */
export const CHAT_FONT_LOAD_LIMIT_MS = 3000

/**
 * The size of the font shorthand that the loads take. A font face has no size of its
 * own, so any length selects the same faces.
 */
const FONT_SPEC_SIZE = '1em'

export interface ChatFonts {
  /** The first load of every face settled, or the limit passed. Staging waits for this. */
  ready: Accessor<boolean>
  /** Every face loaded. The height key holds this. */
  loaded: Accessor<boolean>
}

function sameFamilies(a: readonly string[], b: readonly string[]): boolean {
  return a.length === b.length && a.every((family, i) => family === b[i])
}

/**
 * Load the four faces of each font family of the chat, so that no row measures its
 * text in a fallback font that the browser replaces afterwards.
 *
 * `families` gives CSS `font-family` values. An empty value is skipped. A change of
 * the families (by value) starts again, and the loads and the timers of the run
 * before it change no state.
 */
export function createChatFonts(families: Accessor<readonly string[]>): ChatFonts {
  const [ready, setReady] = createSignal(true)
  const [loaded, setLoaded] = createSignal(true)
  const familyList = createMemo<readonly string[]>(
    () => families().filter(family => family !== ''),
    [],
    { equals: sameFamilies },
  )

  // The number of the current run. A load or a timer of an earlier run compares its own
  // number with this one, and changes nothing when they differ.
  let run = 0
  const timers = new Set<ReturnType<typeof setTimeout>>()
  const clearTimers = () => {
    for (const timer of timers)
      clearTimeout(timer)
    timers.clear()
  }

  const start = (list: readonly string[]) => {
    const current = ++run
    clearTimers()
    const specs = list.flatMap(family => fontVariantSpecs(family, FONT_SPEC_SIZE))
    if (typeof document === 'undefined' || !document.fonts || specs.every(fontVariantAvailable)) {
      batch(() => {
        setReady(true)
        setLoaded(true)
      })
      return
    }
    batch(() => {
      setReady(false)
      setLoaded(false)
    })

    const isCurrent = () => current === run
    const later = (delayMs: number, callback: () => void) => {
      const timer = setTimeout(() => {
        timers.delete(timer)
        if (isCurrent())
          callback()
      }, delayMs)
      timers.add(timer)
    }

    let firstTriesPending = specs.length
    let facesPending = specs.length
    const tryLoad = (spec: string, retriesUsed: number) => {
      void tryLoadFontVariant(spec).then((faceLoaded) => {
        if (!isCurrent())
          return
        // One batch: a reader must never see the fonts as loaded and not ready.
        batch(() => {
          if (retriesUsed === 0 && --firstTriesPending === 0)
            setReady(true)
          if (faceLoaded && --facesPending === 0)
            setLoaded(true)
        })
        // A face that used all its retries leaves `loaded` unset, so a font that never
        // loads changes no height key.
        if (!faceLoaded && retriesUsed < FONT_LOAD_MAX_RETRIES)
          later(FONT_LOAD_RETRY_BASE_MS * 2 ** retriesUsed, () => tryLoad(spec, retriesUsed + 1))
      })
    }

    later(CHAT_FONT_LOAD_LIMIT_MS, () => setReady(true))
    for (const spec of specs)
      tryLoad(spec, 0)
  }

  // A computation, not an effect: the two states must hold their value for the new
  // families before any reader of the same update runs. An effect would let the hidden
  // premeasure copies render once with the states of the old families.
  createComputed(() => {
    const list = familyList()
    untrack(() => start(list))
  })

  onCleanup(() => {
    run++
    clearTimers()
  })

  return { ready, loaded }
}
