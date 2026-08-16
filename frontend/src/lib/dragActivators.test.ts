import { createRoot, createSignal } from 'solid-js'
import { describe, expect, it, vi } from 'vitest'
import { attachDragActivators, rowBodyActivators } from '~/lib/dragActivators'
import { flush } from '~/test-support/async'
import { pointerEvent } from '~/test-support/pointer'

/** Run `fn` under an owner and hand back its dispose, like the app does. */
function setup(fn: () => void): () => void {
  let dispose!: () => void
  createRoot((d) => {
    dispose = d
    fn()
  })
  return () => dispose()
}

/** A press whose target is `el` (or `undefined`, like a bare constructor call). */
function pressOn(pointerType: string, el?: Element): PointerEvent {
  const event = pointerEvent('pointerdown', { pointerType })
  if (el)
    Object.defineProperty(event, 'target', { value: el })
  return event
}

describe('rowBodyActivators', () => {
  it('passes a mouse press that starts on the row itself', () => {
    const handler = vi.fn()
    const activators = rowBodyActivators({ onPointerdown: handler })
    const row = document.createElement('div')

    activators.onPointerdown(pressOn('mouse', row))

    expect(handler).toHaveBeenCalledOnce()
    expect(handler).toHaveBeenCalledWith(expect.objectContaining({ pointerType: 'mouse' }))
  })

  it('swallows a touch press', () => {
    const handler = vi.fn()
    const activators = rowBodyActivators({ onPointerdown: handler })
    const row = document.createElement('div')

    activators.onPointerdown(pressOn('touch', row))

    expect(handler).not.toHaveBeenCalled()
  })

  it('passes a pen press — a pen is a fine pointer like a mouse', () => {
    const handler = vi.fn()
    const activators = rowBodyActivators({ onPointerdown: handler })
    const row = document.createElement('div')

    activators.onPointerdown(pressOn('pen', row))

    expect(handler).toHaveBeenCalledOnce()
  })

  it('swallows a mouse press that starts inside an embedded input', () => {
    const handler = vi.fn()
    const activators = rowBodyActivators({ onPointerdown: handler })
    const row = document.createElement('div')
    const input = document.createElement('input')
    row.appendChild(input)

    activators.onPointerdown(pressOn('mouse', input))

    expect(handler).not.toHaveBeenCalled()
  })

  it('swallows a mouse press that starts on an embedded button, such as the close button', () => {
    const handler = vi.fn()
    const activators = rowBodyActivators({ onPointerdown: handler })
    const row = document.createElement('div')
    const button = document.createElement('button')
    row.appendChild(button)

    activators.onPointerdown(pressOn('mouse', button))

    expect(handler).not.toHaveBeenCalled()
  })

  it('swallows a mouse press that starts on a drag grip, so one press activates once', () => {
    const handler = vi.fn()
    const activators = rowBodyActivators({ onPointerdown: handler })
    const row = document.createElement('div')
    const grip = document.createElement('span')
    grip.setAttribute('data-drag-handle', '')
    row.appendChild(grip)

    activators.onPointerdown(pressOn('mouse', grip))

    expect(handler).not.toHaveBeenCalled()
  })

  it('keeps every event name it was given', () => {
    const activators = rowBodyActivators({
      onPointerdown: () => {},
      onPointermove: () => {},
    })

    expect(Object.keys(activators).sort()).toEqual(['onPointerdown', 'onPointermove'])
  })

  it('returns an empty set for empty input', () => {
    const activators = rowBodyActivators({})

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
    await flush()

    el.dispatchEvent(pointerEvent('pointerdown', { pointerType: 'mouse' }))

    expect(handler).toHaveBeenCalledOnce()
    dispose()
  })

  it('blocks touch and passes mouse when touch is block', async () => {
    const handler = vi.fn()
    const el = document.createElement('div')
    const dispose = setup(() => {
      attachDragActivators(() => el, () => ({ onPointerdown: handler }), { touch: 'block' })
    })
    await flush()

    el.dispatchEvent(pointerEvent('pointerdown', { pointerType: 'touch' }))
    el.dispatchEvent(pointerEvent('pointerdown', { pointerType: 'mouse' }))

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
    await flush()

    el.dispatchEvent(pointerEvent('pointerdown', { pointerType: 'touch' }))

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
    await flush()

    el.dispatchEvent(pointerEvent('pointerdown', { pointerType: 'mouse' }))
    expect(first).toHaveBeenCalledOnce()

    setActivators({ onPointerdown: second })
    await flush()
    el.dispatchEvent(pointerEvent('pointerdown', { pointerType: 'mouse' }))

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
    await flush()

    first.dispatchEvent(pointerEvent('pointerdown', { pointerType: 'mouse' }))
    expect(handler).toHaveBeenCalledOnce()

    setEl(second)
    await flush()
    second.dispatchEvent(pointerEvent('pointerdown', { pointerType: 'mouse' }))
    expect(handler).toHaveBeenCalledTimes(2)

    // The re-bind tore the old element's listeners down with it.
    first.dispatchEvent(pointerEvent('pointerdown', { pointerType: 'mouse' }))
    expect(handler).toHaveBeenCalledTimes(2)
    dispose()
  })

  it('removes the listeners when the owner is disposed', async () => {
    const handler = vi.fn()
    const el = document.createElement('div')
    const dispose = setup(() => {
      attachDragActivators(() => el, () => ({ onPointerdown: handler }), { touch: 'allow' })
    })
    await flush()
    dispose()

    el.dispatchEvent(pointerEvent('pointerdown', { pointerType: 'mouse' }))

    expect(handler).not.toHaveBeenCalled()
  })

  it('does nothing while the accessor reports no element', async () => {
    const handler = vi.fn()
    const dispose = setup(() => {
      attachDragActivators(() => undefined, () => ({ onPointerdown: handler }), { touch: 'allow' })
    })
    await flush()
    dispose()

    expect(handler).not.toHaveBeenCalled()
  })
})
