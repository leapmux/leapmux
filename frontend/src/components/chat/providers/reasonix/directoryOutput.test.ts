import { describe, expect, it } from 'vitest'
import { reasonixDirectoryOutput } from './directoryOutput'

describe('reasonix directory output', () => {
  it('keeps a filename that resembles a clip notice', () => {
    const source = reasonixDirectoryOutput('a.ts\t42\n…(100 more chars truncated)\n')
    expect(source.entries).toHaveLength(2)
    expect(source.truncated).toBe(false)
  })

  it('uses only the last tab as the size separator', () => {
    expect(reasonixDirectoryOutput('a\tb.ts\t42\n').entries).toEqual([{ path: 'a\tb.ts', detail: '42 B' }])
  })

  it('does not describe an unavailable size as a negative byte count', () => {
    expect(reasonixDirectoryOutput('a.ts\t-1\n').entries).toEqual([{ path: 'a.ts', detail: undefined }])
  })

  it.each(['', '(empty directory)', '(empty directory tree)'])('recognizes an empty listing %s', (text) => {
    expect(reasonixDirectoryOutput(text).entries).toEqual([])
  })
})
