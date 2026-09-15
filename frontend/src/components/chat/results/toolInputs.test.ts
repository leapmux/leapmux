import { describe, expect, it } from 'vitest'
import { toolInputPaths } from './toolInputs'

describe('tool input paths', () => {
  it('keeps valid path-array entries and falls back from empty aliases', () => {
    expect(toolInputPaths({ path: '', paths: [null, '', '/one', 0, '/two'] })).toEqual(['/one', '/two'])
    expect(toolInputPaths({ paths: [] })).toEqual([])
    expect(toolInputPaths({ filePath: '/one' })).toEqual(['/one'])
  })
})
