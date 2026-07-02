import { createRoot, createSignal } from 'solid-js'
import { afterEach, beforeEach, describe, expect, it } from 'vitest'
import {
  _getFragmentCacheSize,
  _resetFragmentCache,
  applyCachedHtml,
  cachedInnerHtml,
} from './htmlFragmentCache'

describe('htmlfragmentcache', () => {
  beforeEach(() => {
    _resetFragmentCache()
  })

  afterEach(() => {
    _resetFragmentCache()
  })

  it('applies html equivalently to an innerHTML assignment', () => {
    const el = document.createElement('div')
    applyCachedHtml(el, '<p>hello <strong>world</strong></p>')
    expect(el.innerHTML).toBe('<p>hello <strong>world</strong></p>')
  })

  it('parses a distinct html string once and serves clones thereafter', () => {
    const a = document.createElement('div')
    const b = document.createElement('div')
    applyCachedHtml(a, '<p>shared</p>')
    expect(_getFragmentCacheSize()).toBe(1)
    applyCachedHtml(b, '<p>shared</p>')
    expect(_getFragmentCacheSize()).toBe(1) // one template, two applications
    // Clones: same serialized content, DIFFERENT nodes (a live node can't be
    // in two places; sharing would rip it out of the first element).
    expect(b.innerHTML).toBe(a.innerHTML)
    expect(b.firstChild).not.toBe(a.firstChild)
  })

  it('replaces previous children on re-application', () => {
    const el = document.createElement('div')
    applyCachedHtml(el, '<p>first</p>')
    applyCachedHtml(el, '<p>second</p>')
    expect(el.innerHTML).toBe('<p>second</p>')
    expect(_getFragmentCacheSize()).toBe(2)
  })

  it('applies an oversized html string without caching its parsed subtree', () => {
    const el = document.createElement('div')
    const huge = `<pre>${'x'.repeat(300 * 1024)}</pre>`
    applyCachedHtml(el, huge)
    expect(el.innerHTML).toBe(huge)
    expect(_getFragmentCacheSize()).toBe(0)
  })

  // NOTE: signal writes are asserted OUTSIDE the createRoot callback — the
  // callback body runs inside a batch, so a write within it would defer the
  // render-effect re-run past the assertions (a vacuous pass for the
  // skip case, a false failure for the re-apply case).

  it('cachedInnerHtml skips the DOM when a re-evaluation yields the same string', () => {
    const [flip, setFlip] = createSignal(0)
    // Depends on `flip` but always yields the same string -- the equality
    // guard must keep the exact nodes (text-selection stability relies on
    // unchanged bodies never being re-cloned).
    const html = () => `<p>stable ${flip() >= 0 ? 'y' : 'n'}</p>`
    const el = document.createElement('div')
    let dispose!: () => void
    createRoot((d) => {
      dispose = d
      cachedInnerHtml(html)(el)
    })
    const firstChild = el.firstChild
    expect(firstChild).toBeTruthy()
    setFlip(1)
    expect(el.firstChild).toBe(firstChild) // same node, untouched
    dispose()
  })

  it('cachedInnerHtml re-applies when the string actually changes', () => {
    const [text, setText] = createSignal('one')
    const el = document.createElement('div')
    let dispose!: () => void
    createRoot((d) => {
      dispose = d
      cachedInnerHtml(() => `<p>${text()}</p>`)(el)
    })
    expect(el.innerHTML).toBe('<p>one</p>')
    setText('two')
    expect(el.innerHTML).toBe('<p>two</p>')
    dispose()
  })
})
