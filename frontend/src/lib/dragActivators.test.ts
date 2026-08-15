import { createRoot, createSignal } from 'solid-js'
import { describe, expect, it, vi } from 'vitest'
import { attachDragActivators, finePointerOnlyActivators } from '~/lib/dragActivators'

/**
 * jsdom's PointerEvent constructor ignores the `pointerType` init property in
 * this environment, so the property is defined on the event directly.
 */
function pointerEvent(pointerType: string): PointerEvent {
  const event = new PointerEvent('pointerdown', { bubbles: true })
  Object.defineProperty(event, 'pointerType', { value: pointerType })
  return event
}

/** Run `fn` under an owner and hand back its dispose, like the app does. */
function setup(fn: () => void): () => void {
  let dispose!: () => void
  createRoot((d) => {
    dispose = d
    fn()
  })
  return () => dispose()
}

/** Solid flushes effects on a microtask; two awaits are enough for that. */
async function flushEffects(): Promise<void> {
  await Promise.resolve()
  await Promise.resolve()
}

describe('finePointerOnlyActivators', () => {
  it('passes a mouse press through to the wrapped handler', () => {
    const handler = vi.fn()
    const activators = finePointerOnlyActivators({ onPointerdown: handler })

    activators.onPointerdown(pointerEvent('mouse'))

    expect(handler).toHaveBeenCalledOnce()
    expect(handler).toHaveBeenCalledWith(expect.objectContaining({ pointerType: 'mouse' }))
  })

  it('swallows a touch press', () => {
    const handler = vi.fn()
    const activators = finePointerOnlyActivators({ onPointerdown: handler })

    activators.onPointerdown(pointerEvent('touch'))

    expect(handler).not.toHaveBeenCalled()
  })

  it('passes a pen press through — a pen is a fine pointer like a mouse', () => {
    const handler = vi.fn()
    const activators = finePointerOnlyActivators({ onPointerdown: handler })

    activators.onPointerdown(pointerEvent('pen'))

    expect(handler).toHaveBeenCalledOnce()
  })

  it('keeps every event name it was given', () => {
    const activators = finePointerOnlyActivators({
      onPointerdown: () => {},
      onPointermove: () => {},
    })

    expect(Object.keys(activators).sort()).toEqual(['onPointerdown', 'onPointermove'])
  })

  it('returns an empty set for empty input', () => {
    const activators = finePointerOnlyActivators({})

    expect(activators).toEqual({})
  })
})

describe('attachDragActivators', () => {
  it('binds each handler key as a native listener (onPointerdown -> pointerdown)', async () => {
    const handler = vi.fn()
    const el = document.createElement('div')
    const dispose = setup(() => {
      attachDragActivators(() => el, () => ({ onPointerdown: handler }), { touch: 'allow' })
    })
    await flushEffects()

    el.dispatchEvent(pointerEvent('mouse'))

    expect(handler).toHaveBeenCalledOnce()
    dispose()
  })

  it('blocks touch and passes mouse when touch is block', async () => {
    const handler = vi.fn()
    const el = document.createElement('div')
    const dispose = setup(() => {
      attachDragActivators(() => el, () => ({ onPointerdown: handler }), { touch: 'block' })
    })
    await flushEffects()

    el.dispatchEvent(pointerEvent('touch'))
    el.dispatchEvent(pointerEvent('mouse'))

    expect(handler).toHaveBeenCalledOnce()
    expect(handler).toHaveBeenCalledWith(expect.objectContaining({ pointerType: 'mouse' }))
    dispose()
  })

  it('passes touch through when touch is allow', async () => {
    const handler = vi.fn()
    const el = document.createElement('div')
    const dispose = setup(() => {
      attachDragActivators(() => el, () => ({ onPointerdown: handler }), { touch: 'allow' })
    })
    await flushEffects()

    el.dispatchEvent(pointerEvent('touch'))

    expect(handler).toHaveBeenCalledOnce()
    dispose()
  })

  it('re-binds when the accessor returns new handlers', async () => {
    const first = vi.fn()
    const second = vi.fn()
    const el = document.createElement('div')
    const [activators, setActivators] = createSignal({ onPointerdown: first })
    const dispose = setup(() => {
      attachDragActivators(() => el, activators, { touch: 'allow' })
    })
    await flushEffects()

    el.dispatchEvent(pointerEvent('mouse'))
    expect(first).toHaveBeenCalledOnce()

    setActivators({ onPointerdown: second })
    await flushEffects()
    el.dispatchEvent(pointerEvent('mouse'))

    expect(first).toHaveBeenCalledOnce()
    expect(second).toHaveBeenCalledOnce()
    dispose()
  })

  it('re-binds when the accessor returns a new element', async () => {
    const handler = vi.fn()
    const first = document.createElement('div')
    const second = document.createElement('div')
    const [el, setEl] = createSignal(first)
    const dispose = setup(() => {
      attachDragActivators(el, () => ({ onPointerdown: handler }), { touch: 'allow' })
    })
    await flushEffects()

    first.dispatchEvent(pointerEvent('mouse'))
    expect(handler).toHaveBeenCalledOnce()

    setEl(second)
    await flushEffects()
    second.dispatchEvent(pointerEvent('mouse'))
    expect(handler).toHaveBeenCalledTimes(2)

    // The re-bind tore the old element's listeners down with it.
    first.dispatchEvent(pointerEvent('mouse'))
    expect(handler).toHaveBeenCalledTimes(2)
    dispose()
  })

  it('removes the listeners when the owner is disposed', async () => {
    const handler = vi.fn()
    const el = document.createElement('div')
    const dispose = setup(() => {
      attachDragActivators(() => el, () => ({ onPointerdown: handler }), { touch: 'allow' })
    })
    await flushEffects()
    dispose()

    el.dispatchEvent(pointerEvent('mouse'))

    expect(handler).not.toHaveBeenCalled()
  })

  it('does nothing while the accessor reports no element', async () => {
    const handler = vi.fn()
    const dispose = setup(() => {
      attachDragActivators(() => undefined, () => ({ onPointerdown: handler }), { touch: 'allow' })
    })
    await flushEffects()
    dispose()

    expect(handler).not.toHaveBeenCalled()
  })
})
