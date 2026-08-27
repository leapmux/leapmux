/// <reference types="vitest/globals" />

// A controllable IntersectionObserver, for the tests that drive a reveal.
//
// jsdom implements none, and `createRevealed` treats a missing API as "already
// revealed" -- the behaviour before the latch existed. So without this stub a
// test can never observe the DEFERRED state, and the guard it thinks it wrote
// passes without testing anything.
//
// Same shape as `resizeObserverStub`: install it in `beforeAll`, then reveal
// the element the test cares about.

interface Observed {
  observer: ControllableIntersectionObserver
  target: Element
}

let observed: Observed[] = []

class ControllableIntersectionObserver {
  private callback: IntersectionObserverCallback

  constructor(cb: IntersectionObserverCallback) {
    this.callback = cb
  }

  observe(target: Element) {
    observed.push({ observer: this, target })
  }

  unobserve(target: Element) {
    observed = observed.filter(o => !(o.observer === this && o.target === target))
  }

  disconnect() {
    observed = observed.filter(o => o.observer !== this)
  }

  deliver(target: Element) {
    this.callback(
      [{ target, isIntersecting: true } as IntersectionObserverEntry],
      this as unknown as IntersectionObserver,
    )
  }
}

/** Replace the global with the stub, and forget every previous observation. */
export function installControllableIntersectionObserver() {
  observed = []
  globalThis.IntersectionObserver = ControllableIntersectionObserver as unknown as typeof IntersectionObserver
}

/** Remove the stub, so the next file sees the environment it expects. */
export function removeControllableIntersectionObserver() {
  observed = []
  Reflect.deleteProperty(globalThis, 'IntersectionObserver')
}

/** How many elements are under observation right now. */
export function intersectionObservedCount(): number {
  return observed.length
}

/** Report every observed element as visible. */
export function revealObservedElements() {
  for (const { observer, target } of [...observed])
    observer.deliver(target)
}
