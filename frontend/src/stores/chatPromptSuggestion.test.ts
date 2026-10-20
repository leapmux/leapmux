import { createRenderEffect, createRoot } from 'solid-js'
import { describe, expect, it } from 'vitest'
import { createPromptSuggestionStore } from './chatPromptSuggestion'

describe('createPromptSuggestionStore', () => {
  it('sets and clears a suggestion without changing another agent', () => {
    const store = createPromptSuggestionStore()
    expect(store.get('a')).toBeUndefined()
    store.set('a', { text: 'run the tests' })
    store.set('b', { text: 'review the diff' })
    store.clear('a')
    store.clear('a')
    expect(store.get('a')).toBeUndefined()
    expect(store.get('b')).toEqual({ text: 'review the diff' })
  })

  it('replaces the text without changing the caller object', () => {
    const store = createPromptSuggestionStore()
    const first = { text: 'first' }
    store.set('a', first)
    store.set('a', { text: 'second' })
    expect(store.get('a')).toEqual({ text: 'second' })
    expect(first).toEqual({ text: 'first' })
  })

  it('notifies readers when the suggestion changes or leaves', () => {
    const store = createPromptSuggestionStore()
    const seen: (string | undefined)[] = []
    const dispose = createRoot((disposeRoot) => {
      createRenderEffect(() => seen.push(store.get('a')?.text))
      return disposeRoot
    })
    try {
      store.set('a', { text: 'first' })
      store.set('a', { text: 'second' })
      store.clear('a')
      expect(seen).toEqual([undefined, 'first', 'second', undefined])
    }
    finally {
      dispose()
    }
  })
})
