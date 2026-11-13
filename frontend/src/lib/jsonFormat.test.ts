import { describe, expect, it } from 'vitest'
import { FRACTURED_JSON_MAX_LENGTH, prettifyArgsJson, prettifyJson } from './jsonFormat'

describe('prettifyJson', () => {
  it('formats JSON for a narrow control without changing its values', () => {
    const input = '{"query":"answer","path":"sample.py","limit":20}'
    const formatted = prettifyJson(input, 28)
    expect(formatted.trim().split('\n').length).toBeGreaterThan(2)
    expect(formatted.trim().split('\n').every(line => line.length <= 28)).toBe(true)
    expect(JSON.parse(formatted)).toEqual(JSON.parse(input))
    expect(prettifyJson(input).trim().split('\n')).toHaveLength(1)
  })

  it('formats plain objects', () => {
    expect(prettifyJson({
      a: 1,
      b: { c: true },
    })).toContain('{\n')
    expect(prettifyJson({
      a: 1,
      b: { c: true },
    })).toContain('"a": 1')
  })

  it('formats arrays', () => {
    expect(prettifyJson([
      { a: 1 },
      { b: 2 },
    ])).toContain('"a": 1')
  })

  it('formats valid json strings', () => {
    expect(prettifyJson('{"a":1,"b":{"c":true}}')).toContain('{\n')
    expect(prettifyJson('{"a":1,"b":{"c":true}}')).toContain('"b":')
  })

  it('returns invalid json strings unchanged', () => {
    expect(prettifyJson('not json')).toBe('not json')
  })

  it('returns undefined and function values as strings', () => {
    expect(prettifyJson(undefined)).toBe('undefined')
    expect(prettifyJson(() => 1)).toContain('=>')
  })

  describe('a long text', () => {
    // `[0,0,...,0,10]` is 2n + 4 characters long. FracturedJson packs its numbers
    // into rows; the plain layout puts each number on a line of its own.
    const zeros = (FRACTURED_JSON_MAX_LENGTH - 4) / 2
    const atLimit = `[${'0,'.repeat(zeros)}10]`
    const overLimit = `[${'0,'.repeat(zeros)}100]`

    /**
     * `json` behind enough leading whitespace to take the plain layout, even when
     * `json` is empty. JSON ignores the whitespace, so the text holds the same tokens.
     */
    function padded(json: string): string {
      return `${' '.repeat(FRACTURED_JSON_MAX_LENGTH + 1)}${json}`
    }

    it('lays out a text at the limit with FracturedJson', () => {
      expect(atLimit).toHaveLength(FRACTURED_JSON_MAX_LENGTH)
      const formatted = prettifyJson(atLimit)
      expect(formatted.trim().split('\n').length).toBeLessThan(zeros / 4)
      expect(JSON.parse(formatted)).toEqual(JSON.parse(atLimit))
    })

    it('lays out a text over the limit as the native indentation does', () => {
      expect(overLimit).toHaveLength(FRACTURED_JSON_MAX_LENGTH + 1)
      expect(prettifyJson(overLimit)).toBe(JSON.stringify(JSON.parse(overLimit), null, 4))
    })

    it('lays out a value whose JSON is over the limit as the native indentation does', () => {
      const items = JSON.parse(overLimit) as number[]
      const value = {
        items,
        nested: { empty: {}, list: [], text: 'a "quoted" \\ value' },
      }
      expect(prettifyJson(value)).toBe(JSON.stringify(value, null, 4))
    })

    it('returns a text over the limit unchanged when it is not JSON', () => {
      const text = `not json ${'x'.repeat(FRACTURED_JSON_MAX_LENGTH)}`
      expect(prettifyJson(text)).toBe(text)
    })

    it('copies each number as the text spells it', () => {
      const text = padded('{"wide":9007199254740993,"huge":1e400,"zero":-0,"cents":1.50,"exp":1E5,"small":-1.5e-3,"plain":0}')
      expect(prettifyJson(text)).toBe([
        '{',
        '    "wide": 9007199254740993,',
        '    "huge": 1e400,',
        '    "zero": -0,',
        '    "cents": 1.50,',
        '    "exp": 1E5,',
        '    "small": -1.5e-3,',
        '    "plain": 0',
        '}',
      ].join('\n'))
    })

    it('copies each string with its escapes, and reads no bracket inside a string as structure', () => {
      const text = padded(String.raw`["A\/","\u0041","q\"uote","back\\slash","new\nline","{[,:]}"]`)
      expect(prettifyJson(text)).toBe([
        '[',
        String.raw`    "A\/",`,
        String.raw`    "\u0041",`,
        String.raw`    "q\"uote",`,
        String.raw`    "back\\slash",`,
        String.raw`    "new\nline",`,
        '    "{[,:]}"',
        ']',
      ].join('\n'))
    })

    it('keeps every member of an object in its order, a repeated key too', () => {
      const text = padded('{"b":1,"2":2,"key":1,"key":2}')
      expect(prettifyJson(text)).toBe([
        '{',
        '    "b": 1,',
        '    "2": 2,',
        '    "key": 1,',
        '    "key": 2',
        '}',
      ].join('\n'))
    })

    it('writes an empty object or array on one line, whatever whitespace it holds', () => {
      expect(prettifyJson(padded('{ \n }'))).toBe('{}')
      expect(prettifyJson(padded('[\t]'))).toBe('[]')
      expect(prettifyJson(padded('{"a":{},"b":[ ],"c":[{}]}'))).toBe([
        '{',
        '    "a": {},',
        '    "b": [],',
        '    "c": [',
        '        {}',
        '    ]',
        '}',
      ].join('\n'))
    })

    it('indents each level of nesting, and drops the whitespace of the text', () => {
      const text = padded('[ [1, [2]] ,\n{"a" : {"b":[true,false,null]}}\r\n]  ')
      expect(prettifyJson(text)).toBe([
        '[',
        '    [',
        '        1,',
        '        [',
        '            2',
        '        ]',
        '    ],',
        '    {',
        '        "a": {',
        '            "b": [',
        '                true,',
        '                false,',
        '                null',
        '            ]',
        '        }',
        '    }',
        ']',
      ].join('\n'))
    })

    it('writes a scalar at the top as its own token', () => {
      expect(prettifyJson(padded('"text"'))).toBe('"text"')
      expect(prettifyJson(padded('12345678901234567890'))).toBe('12345678901234567890')
      expect(prettifyJson(padded('null '))).toBe('null')
    })

    it('agrees with JSON.parse on which texts are JSON', () => {
      const texts = [
        '{"a":1}',
        '[1,2]',
        '"\\u12ab"',
        '-0.0e-0',
        '"\u{1F600} and a lone \uD800"',
        '"\u007F"',
        '',
        ' ',
        '[1,]',
        '{"a":1,}',
        '{"a" 1}',
        '{"a":1 "b":2}',
        '{1:2}',
        '{[1]:2}',
        '{"a":}',
        '{a:1}',
        '{\'a\':1}',
        '"abc',
        '[1,2',
        '[1]]',
        '[1}',
        '{"a":1]',
        '1 2',
        '01',
        '-01',
        '1.',
        '.5',
        '+1',
        '-',
        '1e',
        '1e+',
        'tru',
        'nul',
        'truex',
        'NaN',
        'Infinity',
        '"a\u0001b"',
        '"\\x"',
        '"\\u12"',
        '"\\u12g4"',
        '\uFEFF{}',
        '[,1]',
        '{,}',
        ':',
        // JSON whitespace is the space, the tab, the line feed and the carriage
        // return, and nothing else.
        '\t\r\n 1 \n',
        '\u000B1',
        '1\f',
        '\u00A01',
        '[1,\u20282]',
        // A hex escape takes either case, and an exponent either case and sign.
        '"\\u00AF\\uabCD"',
        '"\\u00AG"',
        '[1E+5,2e-5,3E5,-0.0]',
        '1E',
        '1.e5',
      ]
      for (const json of texts) {
        let valid = true
        try {
          JSON.parse(json)
        }
        catch {
          valid = false
        }
        const text = padded(json)
        expect(prettifyJson(text) !== text, JSON.stringify(json)).toBe(valid)
      }
    })

    it('returns a text unchanged, and at once, when its layout would grow with the square of its depth', () => {
      const depth = 100_000
      const text = `${'['.repeat(depth)}${']'.repeat(depth)}`
      // A layout of this text grows with the square of its depth, so it shows
      // as it is, and the layout returns at once instead of building gigabytes.
      expect(prettifyJson(text)).toBe(text)
    })

    it('lays out a text that nests deeply but stays within the growth limit', () => {
      const depth = 200
      const text = padded(`${'['.repeat(depth)}${']'.repeat(depth)}`)
      const formatted = prettifyJson(text)
      expect(formatted).not.toBe(text)
      expect(formatted.split('\n')).toHaveLength(2 * depth - 1)
      expect(JSON.parse(formatted)).toEqual(JSON.parse(text))
    })
  })
})

describe('prettifyArgsJson', () => {
  it('returns empty string for null and undefined', () => {
    expect(prettifyArgsJson(null)).toBe('')
    expect(prettifyArgsJson(undefined)).toBe('')
  })

  it('returns empty string for an empty object', () => {
    expect(prettifyArgsJson({})).toBe('')
  })

  it('prettifies non-empty objects', () => {
    const out = prettifyArgsJson({ a: 1 })
    expect(out).toContain('"a": 1')
  })

  it('prettifies arrays (not treated as empty)', () => {
    expect(prettifyArgsJson([1, 2, 3])).toContain('1')
    expect(prettifyArgsJson([])).toContain('[]')
  })

  it('passes through scalars to prettifyJson', () => {
    expect(prettifyArgsJson('hello')).toBe('hello')
    expect(prettifyArgsJson(42).trim()).toBe('42')
  })
})
