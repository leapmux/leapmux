// @vitest-environment jsdom
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { disposeNativeControlObservation, installNativeControlObservation, readNativeControlObservation } from './nativeControlObservation'

beforeEach(() => {
  document.body.replaceChildren()
})

afterEach(() => {
  disposeNativeControlObservation('test')
  disposeNativeControlObservation('second')
  document.body.replaceChildren()
  vi.restoreAllMocks()
})

function control(visible: boolean): HTMLElement {
  const element = document.createElement('div')
  element.dataset.testid = 'elicitation-form'
  const rectangles = Object.assign(visible ? [new DOMRect(0, 0, 1, 1)] : [], { item: () => null })
  vi.spyOn(element, 'getClientRects').mockReturnValue(rectangles)
  document.body.append(element)
  return element
}

describe('installNativeControlObservation', () => {
  it('records a control that already appears before the turn', () => {
    control(true)
    installNativeControlObservation({ id: 'test', testId: 'elicitation-form' })
    expect(readNativeControlObservation('test')).toBe(true)
  })

  it('ignores a hidden premeasure control', () => {
    control(false)
    installNativeControlObservation({ id: 'test', testId: 'elicitation-form' })
    expect(readNativeControlObservation('test')).toBe(false)
  })

  it('ignores a control whose CSS visibility is hidden', () => {
    control(true).style.visibility = 'hidden'
    installNativeControlObservation({ id: 'test', testId: 'elicitation-form' })
    expect(readNativeControlObservation('test')).toBe(false)
  })

  it('keeps a visible control recorded after it disappears', async () => {
    installNativeControlObservation({ id: 'test', testId: 'elicitation-form' })
    const element = control(true)
    await Promise.resolve()
    expect(readNativeControlObservation('test')).toBe(true)
    element.remove()
    await Promise.resolve()
    expect(readNativeControlObservation('test')).toBe(true)
  })

  it('observes an attribute change that shows the actual control', async () => {
    const element = control(true)
    element.style.visibility = 'hidden'
    installNativeControlObservation({ id: 'test', testId: 'elicitation-form' })
    element.style.visibility = 'visible'
    await Promise.resolve()
    expect(readNativeControlObservation('test')).toBe(true)
  })

  it('keeps separate controls and observation IDs independent', async () => {
    installNativeControlObservation({ id: 'test', testId: 'elicitation-form' })
    installNativeControlObservation({ id: 'second', testId: 'dialog-editor' })
    control(true)
    await Promise.resolve()
    expect(readNativeControlObservation('test')).toBe(true)
    expect(readNativeControlObservation('second')).toBe(false)
    disposeNativeControlObservation('test')
    expect(readNativeControlObservation('second')).toBe(false)
  })

  it('cleans its observer state after the proof', () => {
    installNativeControlObservation({ id: 'test', testId: 'elicitation-form' })
    disposeNativeControlObservation('test')
    expect(() => readNativeControlObservation('test')).toThrow('disappeared')
    expect(window.__nativeControlObservations).toBeUndefined()
  })

  it.each(['', 'elicitation-form"]', '#form', 'form name'])('refuses the invalid test ID %s', (testId) => {
    expect(() => installNativeControlObservation({ id: 'test', testId })).toThrow('one test ID')
    expect(window.__nativeControlObservations).toBeUndefined()
  })
})
