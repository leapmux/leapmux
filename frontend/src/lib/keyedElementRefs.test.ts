import { createRoot } from 'solid-js'
import { describe, expect, it } from 'vitest'
import { createKeyedElementRefs } from './keyedElementRefs'

describe('createKeyedElementRefs', () => {
  it('removes an element when its owner is disposed', () => {
    const refs = createKeyedElementRefs<string, HTMLButtonElement>()
    const element = document.createElement('button')
    let dispose!: () => void
    createRoot((rootDispose) => {
      dispose = rootDispose
      refs.register('one', element)
    })

    expect(refs.get('one')).toBe(element)
    dispose()
    expect(refs.get('one')).toBeUndefined()
  })

  it('does not delete a replacement when the old owner is disposed', () => {
    const refs = createKeyedElementRefs<string, HTMLButtonElement>()
    const oldElement = document.createElement('button')
    const newElement = document.createElement('button')
    let disposeOld!: () => void
    let disposeNew!: () => void
    createRoot((dispose) => {
      disposeOld = dispose
      refs.register('one', oldElement)
    })
    createRoot((dispose) => {
      disposeNew = dispose
      refs.register('one', newElement)
    })

    disposeOld()
    expect(refs.get('one')).toBe(newElement)

    disposeNew()
    expect(refs.get('one')).toBeUndefined()
  })
})
