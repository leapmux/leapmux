// jsdom does not implement ResizeObserver. vitest.setup.ts installs an inert stub.
// Install this controllable stub before a test that needs resize callbacks.
// The trigger helpers notify each observer about its observed elements.

let observers: ControllableResizeObserver[] = []

function entryFor(target: Element): ResizeObserverEntry {
  const contentRect = target.getBoundingClientRect()
  // Test entries use the controlled rectangle for every size array.
  // They do not calculate CSS box sizes or physical pixels.
  const size = { inlineSize: contentRect.width, blockSize: contentRect.height }
  return {
    target,
    contentRect,
    borderBoxSize: [size],
    contentBoxSize: [size],
    devicePixelContentBoxSize: [size],
  }
}

class ControllableResizeObserver implements ResizeObserver {
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
    this.callback(targets.map(entryFor), this)
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
  globalThis.ResizeObserver = ControllableResizeObserver
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
