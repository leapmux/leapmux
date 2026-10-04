import { sha256 } from '@noble/hashes/sha2.js'
import { describe, expect, it } from 'vitest'
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

  it('keeps different native content for all three providers', () => {
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

describe('readNativeProviderMark', () => {
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
    expect(() => readNativeProviderMark(source)).toThrow()
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
    expect(() => readNativeProviderMark(source)).toThrow()
  })

  it.each([
    '',
    '<svg viewBox="0 0 24 24"></svg>',
    '<svg viewBox="0 0 0 0"><path d="M0 0"/></svg>',
    '<svg viewBox="0 0 -1 -1"><path d="M0 0"/></svg>',
    '<svg viewBox="0 0 24 25"><path d="M0 0"/></svg>',
    '<svg viewBox="0 0 24 NaN"><path d="M0 0"/></svg>',
    '<svg viewBox="0 0 0x18 0x18"><path d="M0 0"/></svg>',
    '<svg viewBox="0 0 24 24"><path d=""/></svg>',
    '<svg viewBox="0 0 24 24"><path d="M0 0" onload="run()"/></svg>',
    '<svg viewBox="0 0 24 24"><path d="M0 0" d="M1 1"/></svg>',
    '<svg viewBox="0 0 24 24"><script>run()</script></svg>',
    '<svg viewBox="0 0 24 24"><image href="https://example.com/icon.png" width="24" height="24"/></svg>',
    '<svg viewBox="0 0 24 24"><image href="data:image/svg+xml;base64,AAAA" width="24" height="24"/></svg>',
    '<svg viewBox="0 0 24 24"><image href="data:image/png;base64,AAAA" width="0" height="24"/></svg>',
    '<svg viewBox="0 0 24 24"><image href="data:image/png;base64,AAAA" width="0x18" height="24"/></svg>',
    '<svg viewBox="0 0 24 24"><path d="M0 0"/>trailing text</svg>',
    '<svg viewBox="0 0 24 24"><path d="M0 0"/>',
  ])('rejects an incomplete or unsupported asset: %j', (source) => {
    expect(() => readNativeProviderMark(source)).toThrow()
  })

  it('preserves zero coordinates and complete path attributes', () => {
    expect(readNativeProviderMark('<svg viewBox="0 0 24 24"><path d="M0 0h24v24H0z" fill="currentColor" fill-rule="evenodd"/></svg>'))
      .toEqual({ viewBox: '0 0 24 24', elements: [{ type: 'path', attributes: { 'd': 'M0 0h24v24H0z', 'fill': 'currentColor', 'fill-rule': 'evenodd' } }] })
  })
})
