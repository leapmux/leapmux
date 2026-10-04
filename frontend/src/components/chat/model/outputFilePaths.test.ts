import { describe, expect, it } from 'vitest'
import { normalizeOutputFilePaths } from './outputFilePaths'

describe('normalizeOutputFilePaths', () => {
  it('keeps exact path spelling and the first occurrence of each path', () => {
    const paths = [' /native/with spaces/result.log ', 'C:\\native\\結果.log', '/native/one.log', ' /native/with spaces/result.log ']
    const result = normalizeOutputFilePaths(paths)

    expect(result).toEqual(paths.slice(0, 3))
    expect(Object.isFrozen(result)).toBe(true)
    paths[0] = '/changed-after-extraction'
    expect(result[0]).toBe(' /native/with spaces/result.log ')
  })

  it('omits absent text without changing other paths', () => {
    expect(normalizeOutputFilePaths(['', '  ', '\t\n', '/native/valid.log'])).toEqual(['/native/valid.log'])
    expect(normalizeOutputFilePaths([])).toEqual([])
  })

  it('preserves long and HTML-like path text without parsing or reading it', () => {
    const long = `/native/${'long-path-'.repeat(4000)}.log`
    const markup = '/native/<img src=x onerror=alert(1)>.log'
    expect(normalizeOutputFilePaths([long, markup])).toEqual([long, markup])
  })

  it.each([undefined, null, '', 0, -1, false, {}, new Set(['/native/result.log'])])('rejects a non-array value %s', (value) => {
    expect(() => normalizeOutputFilePaths(value)).toThrow(TypeError)
  })

  it.each([undefined, null, 0, -1, Number.NaN, false, {}, ['nested'], '/native/\0invalid'])('rejects an invalid path value %s', (value) => {
    expect(() => normalizeOutputFilePaths(['/native/valid.log', value])).toThrow(TypeError)
  })
})
