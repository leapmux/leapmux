import { createRoot } from 'solid-js'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { createRevealed } from '~/hooks/createRevealed'
import {
  installControllableIntersectionObserver,
  intersectionObservedCount,
  removeControllableIntersectionObserver,
  revealObservedElements,
} from '~/test-support/intersectionObserverStub'

afterEach(() => {
  removeControllableIntersectionObserver()
})

describe('createRevealed', () => {
  it('starts closed and latches open on the first intersection', () => {
    installControllableIntersectionObserver()
    createRoot((dispose) => {
      const reveal = createRevealed()
      expect(reveal.revealed()).toBe(false)

      reveal.observe(document.createElement('div'))
      expect(reveal.revealed()).toBe(false)

      revealObservedElements()
      expect(reveal.revealed()).toBe(true)
      dispose()
    })
  })

  // The latch may only ever DEFER the work, never withhold it.
  //
  // An IntersectionObserver reports nothing for an element with no box, and a
  // settings row whose container has not laid out is exactly that. Without the
  // fallback the deferred component stayed unmounted for the life of the page,
  // so the control behind it -- the theme picker, the keybindings editor --
  // was not late, it was absent.
  it('reveals anyway when the observer never answers', () => {
    vi.useFakeTimers()
    installControllableIntersectionObserver()
    try {
      createRoot((dispose) => {
        const reveal = createRevealed()
        reveal.observe(document.createElement('div'))
        expect(reveal.revealed()).toBe(false)

        // The observer is installed and SILENT: nothing calls its callback.
        vi.advanceTimersByTime(5_000)
        expect(reveal.revealed()).toBe(true)
        dispose()
      })
    }
    finally {
      vi.useRealTimers()
    }
  })

  // The fallback must not outlive the answer it backs up, or a revealed
  // component keeps a timer for nothing.
  it('drops the fallback once the observer answers', () => {
    vi.useFakeTimers()
    installControllableIntersectionObserver()
    try {
      createRoot((dispose) => {
        const reveal = createRevealed()
        reveal.observe(document.createElement('div'))
        revealObservedElements()
        expect(reveal.revealed()).toBe(true)
        expect(vi.getTimerCount()).toBe(0)
        dispose()
      })
    }
    finally {
      vi.useRealTimers()
    }
  })

  // One observer, released the moment it answers. Every deferred component
  // holds one until its element arrives, and a list that recycles rows would
  // otherwise accumulate them.
  it('disconnects once it has its answer', () => {
    installControllableIntersectionObserver()
    createRoot((dispose) => {
      const reveal = createRevealed()
      reveal.observe(document.createElement('div'))
      expect(intersectionObservedCount()).toBe(1)

      revealObservedElements()
      expect(intersectionObservedCount()).toBe(0)
      dispose()
    })
  })

  it('observes nothing more once it is open', () => {
    installControllableIntersectionObserver()
    createRoot((dispose) => {
      const reveal = createRevealed()
      reveal.observe(document.createElement('div'))
      revealObservedElements()

      reveal.observe(document.createElement('div'))
      expect(intersectionObservedCount()).toBe(0)
      expect(reveal.revealed()).toBe(true)
      dispose()
    })
  })

  it('releases the observer when its owner is disposed', () => {
    installControllableIntersectionObserver()
    const dispose = createRoot((disposeRoot) => {
      createRevealed().observe(document.createElement('div'))
      return disposeRoot
    })
    expect(intersectionObservedCount()).toBe(1)
    dispose()
    expect(intersectionObservedCount()).toBe(0)
  })

  // jsdom implements none, and neither does an engine too old for it. Eager is
  // the behaviour without this latch, so a missing API costs a load nobody
  // asked for rather than a component that never renders at all.
  it('counts the element as revealed where the API is absent', () => {
    removeControllableIntersectionObserver()
    createRoot((dispose) => {
      const reveal = createRevealed()
      reveal.observe(document.createElement('div'))
      expect(reveal.revealed()).toBe(true)
      dispose()
    })
  })
})
