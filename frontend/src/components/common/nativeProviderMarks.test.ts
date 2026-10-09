import { sha256 } from '@noble/hashes/sha2.js'
import { describe, expect, it } from 'vitest'
import { pngBase64 } from '~/test-support/pngFixture'
import museCode from '../../../../icons/agents/muse-code.svg?raw'
import { NATIVE_PROVIDER_MARKS, readNativeProviderMark } from './nativeProviderMarks'

describe('NATIVE_PROVIDER_MARKS', () => {
  it.each(Object.entries(NATIVE_PROVIDER_MARKS))('keeps a complete square native asset for %s', (_name, mark) => {
    const values = mark.viewBox.split(/\s+/).map(Number)
    expect(values).toHaveLength(4)
    expect(values.every(Number.isFinite)).toBe(true)
    expect(values[0]).toBe(0)
    expect(values[1]).toBe(0)
    expect(values[2]).toBeGreaterThan(0)
    expect(values[2]).toBe(values[3])
    expect(mark.elements.length).toBeGreaterThan(0)
    for (const element of mark.elements) {
      expect(['path', 'image']).toContain(element.type)
      expect(element.attributes).not.toHaveProperty('id')
      for (const [name, value] of Object.entries(element.attributes)) {
        expect(name).not.toMatch(/^on/i)
        if (name === 'href')
          expect(value).toMatch(/^data:image\/png;base64,[A-Za-z0-9+/]+=*$/)
      }
    }
  })

  it('keeps different native content for all providers', () => {
    const contents = Object.values(NATIVE_PROVIDER_MARKS).map(mark => JSON.stringify(mark.elements))
    expect(new Set(contents).size).toBe(contents.length)
  })

  it('preserves the complete original Gemini PNG without remote resources', () => {
    const source = NATIVE_PROVIDER_MARKS.geminiCli.elements.find(element => element.type === 'image')?.attributes.href
    expect(source).toBeDefined()
    const bytes = Uint8Array.from(atob(source!.slice('data:image/png;base64,'.length)), character => character.charCodeAt(0))
    expect(bytes).toHaveLength(46_696)
    expect(Array.from(bytes.slice(0, 8))).toEqual([137, 80, 78, 71, 13, 10, 26, 10])
    expect(Array.from(bytes.slice(-8))).toEqual([73, 69, 78, 68, 174, 66, 96, 130])
    // The source project's PNG supplied this digest before the SVG wrapper existed.
    const digest = Array.from(sha256(bytes), byte => byte.toString(16).padStart(2, '0')).join('')
    expect(digest).toBe('351e9f5b1bf863d738cd7be4ed040a625a1419450ae7fc490143e4042b7c2438')
  })
})

// The reasons that `readNativeProviderMark` gives for a refusal, each a substring of its error.
const NO_ROOT = 'requires an SVG root with a viewBox and content'
const NO_SQUARE_VIEW_BOX = 'requires a positive square viewBox at the origin'
const NO_PATH_GEOMETRY = 'path requires its complete geometry'
const BAD_ATTRIBUTE = 'contains an unsupported or repeated attribute'
const BAD_CONTENT = 'contains unsupported SVG content'
const BAD_IMAGE = 'image requires canonical base64, a PNG header, and positive dimensions'

/** A canonical PNG data URI with a header that the dimension sniffer reads, so only the width can fail a case. */
const PNG_HREF = `data:image/png;base64,${pngBase64(24, 24)}`

describe('readNativeProviderMark', () => {
  it('preserves the Muse source geometry and gradient paint', () => {
    const mark = readNativeProviderMark(museCode)
    expect(mark.viewBox).toBe('0 0 100 100')
    expect(mark.elements).toHaveLength(1)
    expect(mark.elements[0]?.attributes.d).toBe(/<path d="([^"]+)"/.exec(museCode)?.[1])
    expect(mark.linearGradients).toEqual([{
      id: 'paint0_linear_454_562',
      attributes: { x1: '133.245', y1: '109.046', x2: '24.9203', y2: '-12.5565', gradientUnits: 'userSpaceOnUse' },
      stops: [
        { 'offset': '0.254808', 'stop-color': '#0082FB', 'style': 'stop-color:#0082FB;stop-color:color(display-p3 0.0000 0.5098 0.9843);stop-opacity:1;' },
        { 'offset': '0.697115', 'stop-color': '#0064E0', 'style': 'stop-color:#0064E0;stop-color:color(display-p3 0.0000 0.3922 0.8784);stop-opacity:1;' },
        { 'offset': '1', 'stop-color': '#0040DC', 'style': 'stop-color:#0040DC;stop-color:color(display-p3 0.0000 0.2510 0.8627);stop-opacity:1;' },
      ],
    }])
  })

  it.each([
    ['duplicate IDs', '<linearGradient id="g"><stop offset="0" stop-color="#000"/></linearGradient><linearGradient id="g"><stop offset="1" stop-color="#fff"/></linearGradient>'],
    ['unsupported gradient attribute', '<linearGradient id="g" href="https://example.com/paint"><stop offset="0" stop-color="#000"/></linearGradient>'],
    ['NaN coordinate', '<linearGradient id="g" x1="NaN"><stop offset="0" stop-color="#000"/></linearGradient>'],
    ['infinite coordinate', '<linearGradient id="g" y1="Infinity"><stop offset="0" stop-color="#000"/></linearGradient>'],
    ['hexadecimal coordinate', '<linearGradient id="g" x2="0x10"><stop offset="0" stop-color="#000"/></linearGradient>'],
    ['negative offset', '<linearGradient id="g"><stop offset="-0.1" stop-color="#000"/></linearGradient>'],
    ['large offset', '<linearGradient id="g"><stop offset="1.1" stop-color="#000"/></linearGradient>'],
    ['unsorted offsets', '<linearGradient id="g"><stop offset="1" stop-color="#000"/><stop offset="0" stop-color="#fff"/></linearGradient>'],
    ['empty gradient', '<linearGradient id="g"></linearGradient>'],
    ['unsupported units', '<linearGradient id="g" gradientUnits="unknown"><stop offset="0" stop-color="#000"/></linearGradient>'],
    ['unsupported stop attribute', '<linearGradient id="g"><stop offset="0" stop-color="#000" onload="run()"/></linearGradient>'],
    ['duplicate stop attribute', '<linearGradient id="g"><stop offset="0" offset="1" stop-color="#000"/></linearGradient>'],
    ['invalid opacity', '<linearGradient id="g"><stop offset="0" stop-color="#000" style="stop-opacity:2;"/></linearGradient>'],
    ['invalid display-p3 color', '<linearGradient id="g"><stop offset="0" stop-color="#000" style="stop-color:color(display-p3 2 0 0);"/></linearGradient>'],
    ['external stop color', '<linearGradient id="g"><stop offset="0" stop-color="url(https://example.com/paint)"/></linearGradient>'],
    ['invalid five-digit color', '<linearGradient id="g"><stop offset="0" stop-color="#12345"/></linearGradient>'],
    ['unsupported style', '<linearGradient id="g"><stop offset="0" stop-color="#000" style="background:url(https://example.com/image);"/></linearGradient>'],
  ])('rejects %s in a native gradient', (_case, gradient) => {
    expect(() => readNativeProviderMark(`<svg viewBox="0 0 24 24"><path d="M0 0h24v24H0z" fill="url(#g)"/><defs>${gradient}</defs></svg>`)).toThrow()
  })

  it.each(['url(#absent)', 'url(https://example.com/paint)', 'url(//example.com/paint)'])('rejects the unresolved paint %s', (fill) => {
    expect(() => readNativeProviderMark(`<svg viewBox="0 0 24 24"><path d="M0 0h24v24H0z" fill="${fill}"/></svg>`)).toThrow()
  })

  it('rejects duplicate definition groups', () => {
    expect(() => readNativeProviderMark('<svg viewBox="0 0 24 24"><path d="M0 0h24v24H0z"/><defs><linearGradient id="g"><stop offset="0" stop-color="#000"/></linearGradient></defs><defs><linearGradient id="h"><stop offset="1" stop-color="#fff"/></linearGradient></defs></svg>')).toThrow()
  })

  it('preserves a four-digit RGBA color', () => {
    const mark = readNativeProviderMark('<svg viewBox="0 0 24 24"><path d="M0 0h24v24H0z" fill="url(#g)"/><defs><linearGradient id="g"><stop offset="0" stop-color="#1234"/></linearGradient></defs></svg>')
    expect(mark.linearGradients?.[0]?.stops[0]?.['stop-color']).toBe('#1234')
  })

  it.each([
    String.raw`u\72l(https://example.com/paint)`,
    String.raw`\75rl(https://example.com/paint)`,
    'linear-gradient(#000,#fff)',
    'unrecognized-paint',
    'red',
  ])('rejects the unsupported paint grammar %s', (fill) => {
    expect(() => readNativeProviderMark(`<svg viewBox="0 0 24 24"><path d="M0 0h24v24H0z" fill="${fill}"/></svg>`)).toThrow()
  })

  it('preserves root paint and an explicit child paint override', () => {
    const mark = readNativeProviderMark('<svg viewBox="0 0 24 24" fill="#f00"><path d="M0 0h12v24H0z"/><path d="M12 0h12v24H12z" fill="#00f"/></svg>')
    expect(mark).toMatchObject({
      fill: '#f00',
      elements: [
        { type: 'path', attributes: { d: 'M0 0h12v24H0z' } },
        { type: 'path', attributes: { d: 'M12 0h12v24H12z', fill: '#00f' } },
      ],
    })
  })

  it('rejects canonical base64 that contains another format instead of PNG bytes', () => {
    const source = `<svg viewBox="0 0 24 24"><image href="data:image/png;base64,${btoa('this is not a PNG')}" width="24" height="24"/></svg>`
    expect(() => readNativeProviderMark(source)).toThrow(BAD_IMAGE)
  })

  it.each(['missing padding', 'extra padding', 'noncanonical unused bits'] as const)('rejects %s in embedded native PNG base64', (kind) => {
    const href = NATIVE_PROVIDER_MARKS.geminiCli.elements.find(element => element.type === 'image')?.attributes.href
    expect(href).toBeDefined()
    const prefix = 'data:image/png;base64,'
    const payload = href!.slice(prefix.length)
    expect(payload.endsWith('==')).toBe(true)
    const invalid = kind === 'missing padding'
      ? payload.replace(/=+$/, '')
      : kind === 'extra padding'
        ? `${payload}=`
        : `${payload.slice(0, -3)}h==`
    const source = `<svg viewBox="0 0 24 24"><image href="${prefix}${invalid}" width="24" height="24"/></svg>`
    expect(() => readNativeProviderMark(source)).toThrow(BAD_IMAGE)
  })

  it.each([
    ['', NO_ROOT],
    ['<svg viewBox="0 0 24 24"></svg>', NO_ROOT],
    ['<svg viewBox="0 0 0 0"><path d="M0 0"/></svg>', NO_SQUARE_VIEW_BOX],
    ['<svg viewBox="0 0 -1 -1"><path d="M0 0"/></svg>', NO_SQUARE_VIEW_BOX],
    ['<svg viewBox="0 0 24 25"><path d="M0 0"/></svg>', NO_SQUARE_VIEW_BOX],
    ['<svg viewBox="0 0 24 NaN"><path d="M0 0"/></svg>', NO_SQUARE_VIEW_BOX],
    ['<svg viewBox="0 0 0x18 0x18"><path d="M0 0"/></svg>', NO_SQUARE_VIEW_BOX],
    ['<svg viewBox="0 0 24 24"><path d=""/></svg>', NO_PATH_GEOMETRY],
    ['<svg viewBox="0 0 24 24"><path d="M0 0" onload="run()"/></svg>', BAD_ATTRIBUTE],
    ['<svg viewBox="0 0 24 24"><path d="M0 0" d="M1 1"/></svg>', BAD_ATTRIBUTE],
    ['<svg viewBox="0 0 24 24"><script>run()</script></svg>', BAD_CONTENT],
    ['<svg viewBox="0 0 24 24"><image href="https://example.com/icon.png" width="24" height="24"/></svg>', BAD_IMAGE],
    ['<svg viewBox="0 0 24 24"><image href="data:image/svg+xml;base64,AAAA" width="24" height="24"/></svg>', BAD_IMAGE],
    [`<svg viewBox="0 0 24 24"><image href="${PNG_HREF}" width="0" height="24"/></svg>`, BAD_IMAGE],
    [`<svg viewBox="0 0 24 24"><image href="${PNG_HREF}" width="0x18" height="24"/></svg>`, BAD_IMAGE],
    ['<svg viewBox="0 0 24 24"><path d="M0 0"/>trailing text</svg>', BAD_CONTENT],
    ['<svg viewBox="0 0 24 24"><path d="M0 0"/>', NO_ROOT],
  ])('rejects an incomplete or unsupported asset: %j', (source, reason) => {
    expect(() => readNativeProviderMark(source)).toThrow(reason)
  })

  it('accepts an embedded PNG with positive dimensions', () => {
    expect(readNativeProviderMark(`<svg viewBox="0 0 24 24"><image href="${PNG_HREF}" width="24" height="24"/></svg>`))
      .toEqual({ viewBox: '0 0 24 24', elements: [{ type: 'image', attributes: { href: PNG_HREF, width: '24', height: '24' } }] })
  })

  it('preserves zero coordinates and complete path attributes', () => {
    expect(readNativeProviderMark('<svg viewBox="0 0 24 24"><path d="M0 0h24v24H0z" fill="currentColor" fill-rule="evenodd"/></svg>'))
      .toEqual({ viewBox: '0 0 24 24', elements: [{ type: 'path', attributes: { 'd': 'M0 0h24v24H0z', 'fill': 'currentColor', 'fill-rule': 'evenodd' } }] })
  })
})
