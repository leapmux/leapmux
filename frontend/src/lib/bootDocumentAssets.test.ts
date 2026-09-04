import type { BootDocumentAsset } from '~/lib/bootDocumentAssets'
import { afterEach, describe, expect, it, vi } from 'vitest'
import {
  BOOT_DEFER_CSS_ATTR,
  bootDeferStylesScript,
  deferredStylesheetAttrs,
  isClientEntryModulepreload,
  partitionBootDocumentAssets,
  resolveClientEntryHref,
} from '~/lib/bootDocumentAssets'

function link(rel: string, href: string, extra: Record<string, unknown> = {}): BootDocumentAsset {
  return { tag: 'link', attrs: { rel, href, ...extra } }
}

describe('isClientEntryModulepreload', () => {
  it('matches the vinxi client entry basename', () => {
    expect(isClientEntryModulepreload('/_build/assets/client-CX-js57o.js')).toBe(true)
    expect(isClientEntryModulepreload('/_build/assets/AuthContext-DrhLy7wf.js')).toBe(false)
    expect(isClientEntryModulepreload('/_build/assets/client-CfHXoPZM.css')).toBe(false)
  })

  it('rejects an empty href', () => {
    expect(isClientEntryModulepreload('')).toBe(false)
  })

  it('accepts a query string on the client entry basename', () => {
    expect(isClientEntryModulepreload('/_build/assets/client-CX-js57o.js?v=1')).toBe(true)
  })

  it('prefers an exact match against the manifest entry path', () => {
    const entry = '/_build/assets/client-abc.js'
    expect(isClientEntryModulepreload(entry, entry)).toBe(true)
    expect(isClientEntryModulepreload('/_build/assets/other-abc.js', entry)).toBe(false)
  })

  it('matches when one side is a trailing path of the other', () => {
    expect(isClientEntryModulepreload(
      './assets/client-abc.js',
      '/_build/assets/client-abc.js',
    )).toBe(true)
  })
})

describe('resolveClientEntryHref', () => {
  it('returns undefined when the vinxi client manifest is absent', () => {
    // Vitest has no MANIFEST; StartServer is what supplies it at prerender.
    expect(resolveClientEntryHref()).toBeUndefined()
  })
})

describe('partitionBootDocumentAssets', () => {
  const assets: BootDocumentAsset[] = [
    link('stylesheet', '/_build/assets/client.css', { fetchPriority: 'high' }),
    link('stylesheet', '/_build/assets/shared.css', { fetchPriority: 'high' }),
    link('modulepreload', '/_build/assets/AuthContext.js'),
    link('modulepreload', '/_build/assets/client-abc123.js'),
    link('modulepreload', '/_build/assets/captcha.js'),
    { tag: 'style', attrs: { id: 'hmr' }, children: '.x{}' },
  ]

  it('returns empty lists for an empty asset array', () => {
    expect(partitionBootDocumentAssets([])).toEqual({
      immediate: [],
      deferredStylesheets: [],
    })
  })

  it('defers every stylesheet and keeps non-preload tags', () => {
    const { immediate, deferredStylesheets } = partitionBootDocumentAssets(assets)

    expect(deferredStylesheets.map(a => a.attrs?.href)).toEqual([
      '/_build/assets/client.css',
      '/_build/assets/shared.css',
    ])
    expect(immediate.some(a => a.tag === 'style')).toBe(true)
  })

  it('keeps icon and other non-stylesheet links in the immediate set', () => {
    const mixed = [
      link('icon', '/icons/leapmux-icon.svg'),
      link('stylesheet', '/_build/assets/app.css'),
      link('modulepreload', '/_build/assets/AuthContext.js'),
    ]
    const { immediate, deferredStylesheets } = partitionBootDocumentAssets(mixed)

    expect(deferredStylesheets).toHaveLength(1)
    expect(immediate.map(a => a.attrs?.rel)).toEqual(['icon'])
  })

  it('keeps only the client entry modulepreload', () => {
    const { immediate } = partitionBootDocumentAssets(
      assets,
      '/_build/assets/client-abc123.js',
    )
    const preloads = immediate.filter(a => a.attrs?.rel === 'modulepreload')

    expect(preloads).toHaveLength(1)
    expect(preloads[0]?.attrs?.href).toBe('/_build/assets/client-abc123.js')
  })

  it('drops every modulepreload when none is the client entry', () => {
    const onlyDeep = [
      link('modulepreload', '/_build/assets/AuthContext.js'),
      link('modulepreload', '/_build/assets/captcha.js'),
    ]
    const { immediate } = partitionBootDocumentAssets(onlyDeep, '/_build/assets/client-missing.js')
    expect(immediate).toEqual([])
  })

  it('drops a modulepreload whose href is missing', () => {
    const broken: BootDocumentAsset = { tag: 'link', attrs: { rel: 'modulepreload' } }
    const { immediate } = partitionBootDocumentAssets([broken])
    expect(immediate).toEqual([])
  })
})

describe('deferredStylesheetAttrs', () => {
  it('sets print media, marks the link, and drops fetchPriority', () => {
    const attrs = deferredStylesheetAttrs({
      rel: 'stylesheet',
      href: '/x.css',
      fetchPriority: 'high',
      key: 'k1',
    })

    expect(attrs.media).toBe('print')
    expect(attrs[BOOT_DEFER_CSS_ATTR]).toBe('')
    expect(attrs.rel).toBe('stylesheet')
    expect(attrs.href).toBe('/x.css')
    expect(attrs.fetchPriority).toBeUndefined()
    expect(attrs.key).toBeUndefined()
  })
})

describe('bootDeferStylesScript', () => {
  afterEach(() => {
    vi.unstubAllGlobals()
    document.head.replaceChildren()
  })

  it('promotes marked links after a double rAF', () => {
    const src = bootDeferStylesScript()
    expect(src).toContain('requestAnimationFrame')
    expect(src).toContain(`link[${BOOT_DEFER_CSS_ATTR}]`)
    expect(src).toContain('media="all"')
    expect(src).toContain('removeAttribute')
  })

  it('promotes deferred stylesheet links in the document', () => {
    const callbacks: FrameRequestCallback[] = []
    vi.stubGlobal('requestAnimationFrame', (cb: FrameRequestCallback) => {
      callbacks.push(cb)
      return callbacks.length
    })

    const tagged = document.createElement('link')
    tagged.rel = 'stylesheet'
    tagged.href = '/deferred.css'
    tagged.media = 'print'
    tagged.setAttribute(BOOT_DEFER_CSS_ATTR, '')
    document.head.append(tagged)

    const unmarked = document.createElement('link')
    unmarked.rel = 'stylesheet'
    unmarked.href = '/already.css'
    unmarked.media = 'all'
    document.head.append(unmarked)

    // eslint-disable-next-line no-new-func -- evaluate the same string the document ships
    new Function(bootDeferStylesScript())()
    expect(callbacks).toHaveLength(1)
    callbacks[0]!(0)
    expect(callbacks).toHaveLength(2)
    callbacks[1]!(0)

    expect(tagged.media).toBe('all')
    expect(tagged.hasAttribute(BOOT_DEFER_CSS_ATTR)).toBe(false)
    expect(unmarked.media).toBe('all')
    expect(unmarked.hasAttribute(BOOT_DEFER_CSS_ATTR)).toBe(false)
  })
})
