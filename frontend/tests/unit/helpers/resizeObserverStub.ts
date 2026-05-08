// Shared controllable ResizeObserver stub for unit tests. jsdom doesn't
// implement ResizeObserver; vitest.setup.ts installs an inert no-op stub.
// Tests that need to drive the resize callback should call
// installControllableResizeObserver() inside beforeAll() to override the
// inert stub with one that tracks every constructed observer's callback,
// then invoke the returned triggerResizeObservers() to fire them all.

let callbacks: ResizeObserverCallback[] = []

class ControllableResizeObserver {
  private callback: ResizeObserverCallback

  constructor(cb: ResizeObserverCallback) {
    this.callback = cb
    callbacks.push(cb)
  }

  observe() {}
  unobserve() {}
  disconnect() {
    const idx = callbacks.indexOf(this.callback)
    if (idx >= 0)
      callbacks.splice(idx, 1)
  }
}

export async function flushAnimationFrame() {
  await new Promise(resolve => requestAnimationFrame(() => resolve(undefined)))
}

export function installControllableResizeObserver() {
  callbacks = []
  globalThis.ResizeObserver = ControllableResizeObserver as unknown as typeof ResizeObserver
}

export async function triggerResizeObservers() {
  for (const cb of [...callbacks])
    cb([], {} as ResizeObserver)
  await flushAnimationFrame()
}

export function triggerResizeObserversSync() {
  for (const cb of [...callbacks])
    cb([], {} as ResizeObserver)
}
