// Shared pointer-event test helpers. jsdom 30 implements PointerEvent natively
// (constructor, `pointerId`, and `instanceof MouseEvent`), so these build real
// events rather than shimming a MouseEvent subclass the way they had to under
// jsdom 29.
//
// Every helper defaults `pointerId` to 1 rather than leaving it 0: the drag
// hooks latch the id from `pointerdown` and ignore later events that don't
// match, so a test that omits it still exercises that filter instead of
// accidentally relying on a falsy id.

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
