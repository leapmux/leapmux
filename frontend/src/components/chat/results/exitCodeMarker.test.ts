import { describe, expect, it } from 'vitest'
import { splitExitCodeMarker } from './exitCodeMarker'

describe('splitExitCodeMarker', () => {
  it('splits the code off the first line', () => {
    expect(splitExitCodeMarker('Exit code 1\nmv: no such file'))
      .toEqual({ output: 'mv: no such file', exitCode: 1 })
  })

  it('reads a multi-digit code', () => {
    expect(splitExitCodeMarker('Exit code 127\nnot found')).toEqual({ output: 'not found', exitCode: 127 })
  })

  // Zero is a real exit code, not an absent one.
  it('reads a zero code', () => {
    expect(splitExitCodeMarker('Exit code 0\ndone')).toEqual({ output: 'done', exitCode: 0 })
  })

  // A marker with nothing after it is the whole result. Inventing a body would be
  // worse than an empty one.
  it('accepts a marker that is the entire result', () => {
    expect(splitExitCodeMarker('Exit code 2')).toEqual({ output: '', exitCode: 2 })
  })

  // Goose sends the marker as its own content block, so joining leaves a blank line
  // where it was. Keeping it would put an empty first line above every failure.
  it('consumes the blank line a separate marker block leaves behind', () => {
    expect(splitExitCodeMarker('exit code: 1\n\nls: no such file'))
      .toEqual({ output: 'ls: no such file', exitCode: 1 })
  })

  it('reads the colon spelling Goose emits', () => {
    expect(splitExitCodeMarker('exit code: 2\nboom')).toEqual({ output: 'boom', exitCode: 2 })
  })

  // The same words inside a command's own output belong to that command.
  it('ignores the words anywhere but the first line', () => {
    const text = 'checking\nExit code 1\n'
    expect(splitExitCodeMarker(text)).toEqual({ output: text })
  })

  it.each([
    'Exit code\nx',
    'Exit code abc\nx',
    'Exit codes 1\nx',
    'exit code 1\nx',
    'Exit code: 1\nx',
    ' Exit code 1\nx',
    'Exit code -1\nx',
  ])('ignores a first line that only resembles the marker: %j', (text) => {
    expect(splitExitCodeMarker(text)).toEqual({ output: text })
  })

  it('leaves empty input alone', () => {
    expect(splitExitCodeMarker('')).toEqual({ output: '' })
  })
})
