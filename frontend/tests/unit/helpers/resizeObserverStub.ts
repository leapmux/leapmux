// Shared controllable ResizeObserver stub for unit tests. jsdom doesn't
// implement ResizeObserver; vitest.setup.ts installs an inert no-op stub.
// Tests that need to drive the resize callback should call
// installControllableResizeObserver() inside beforeAll() to override the
// inert stub with one that tracks every constructed observer's callback and
// observed elements, then invoke the returned trigger helpers.

let observers: ControllableResizeObserver[] = []

function entryFor(target: Element): ResizeObserverEntry {
  return {
    target,
    contentRect: target.getBoundingClientRect(),
  } as ResizeObserverEntry
}

class ControllableResizeObserver {
  private callback: ResizeObserverCallback
  private observed = new Set<Element>()

  constructor(cb: ResizeObserverCallback) {
    this.callback = cb
    observers.push(this)
  }

  observe(target: Element) {
    this.observed.add(target)
  }

  unobserve(target: Element) {
    this.observed.delete(target)
  }

  disconnect() {
    const idx = observers.indexOf(this)
    if (idx >= 0)
      observers.splice(idx, 1)
    this.observed.clear()
  }

  trigger(targets = [...this.observed]) {
    this.callback(targets.map(entryFor), this as unknown as ResizeObserver)
  }

  observes(target: Element): boolean {
    return this.observed.has(target)
  }
}

export async function flushAnimationFrame() {
  await new Promise(resolve => requestAnimationFrame(() => resolve(undefined)))
}

export function installControllableResizeObserver() {
  observers = []
  globalThis.ResizeObserver = ControllableResizeObserver as unknown as typeof ResizeObserver
}

export async function triggerResizeObservers() {
  triggerResizeObserversSync()
  await flushAnimationFrame()
}

export function triggerResizeObserversSync() {
  for (const observer of [...observers])
    observer.trigger()
}

export async function triggerResizeObserverFor(target: Element) {
  triggerResizeObserverForSync(target)
  await flushAnimationFrame()
}

export function triggerResizeObserverForSync(target: Element) {
  for (const observer of [...observers]) {
    if (observer.observes(target))
      observer.trigger([target])
  }
}
