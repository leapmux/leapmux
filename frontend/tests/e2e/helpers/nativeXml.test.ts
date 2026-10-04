import { describe, expect, it } from 'vitest'
import { decodeNativeXmlText, nativeXmlField } from './nativeXml'

describe('decodeNativeXmlText', () => {
  it('decodes the five native entities once', () => {
    expect(decodeNativeXmlText('&lt;&gt;&quot;&apos;&amp;')).toBe('<>"\'&')
    expect(decodeNativeXmlText('&amp;lt;')).toBe('&lt;')
    expect(decodeNativeXmlText('')).toBe('')
  })
})

describe('nativeXmlField', () => {
  it('keeps absent, empty, and multiline fields distinct', () => {
    expect(nativeXmlField('<result></result>', 'result')).toBe('')
    expect(nativeXmlField('<result>first\n&amp;second</result>', 'result')).toBe('first\n&second')
    expect(nativeXmlField('<other>0</other>', 'result')).toBeUndefined()
  })

  it('refuses repeated fields and invalid tag patterns', () => {
    expect(() => nativeXmlField('<result>1</result><result>2</result>', 'result')).toThrow('repeats')
    expect(() => nativeXmlField('<result>1</result>', 'result|other')).toThrow('valid tag')
    expect(() => nativeXmlField('', '')).toThrow('valid tag')
  })
})
