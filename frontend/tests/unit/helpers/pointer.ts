// Shared pointer-event test helpers. jsdom (29.x) does not implement
// PointerEvent; tests that drive drag interactions install a MouseEvent
// subclass tagged with `pointerId` so `instanceof PointerEvent` works
// and the helpers' pointer-id filter behaves naturally.

class FakePointerEvent extends MouseEvent {
  pointerId: number
  constructor(type: string, init: MouseEventInit & { pointerId?: number } = {}) {
    super(type, init)
    this.pointerId = init.pointerId ?? 1
  }
}

export function installPointerEventShim() {
  if (typeof globalThis.PointerEvent === 'undefined')
    globalThis.PointerEvent = FakePointerEvent as unknown as typeof PointerEvent
}

export function stubBoundingRect(el: HTMLElement, width: number, height: number) {
  el.getBoundingClientRect = () => ({
    width,
    height,
    top: 0,
    left: 0,
    right: width,
    bottom: height,
    x: 0,
    y: 0,
    toJSON: () => ({}),
  })
}

interface PointerOpts {
  x?: number
  y?: number
  pointerId?: number
}

function makePointerEvent(type: string, opts: PointerOpts = {}, init: PointerEventInit = {}): PointerEvent {
  return new PointerEvent(type, {
    clientX: opts.x ?? 0,
    clientY: opts.y ?? 0,
    pointerId: opts.pointerId ?? 1,
    bubbles: true,
    ...init,
  })
}

export function dispatchPointerDown(el: HTMLElement, opts: PointerOpts = {}) {
  el.dispatchEvent(makePointerEvent('pointerdown', opts, { cancelable: true }))
}

export function pointerdownEvent(opts: PointerOpts = {}): PointerEvent {
  return makePointerEvent('pointerdown', opts, { cancelable: true })
}

export function dispatchPointerMove(opts: PointerOpts = {}) {
  window.dispatchEvent(makePointerEvent('pointermove', opts))
}

export function dispatchPointerUp(opts: PointerOpts = {}) {
  window.dispatchEvent(makePointerEvent('pointerup', opts))
}

export function dispatchPointerCancel(opts: { pointerId?: number } = {}) {
  window.dispatchEvent(makePointerEvent('pointercancel', opts))
}
