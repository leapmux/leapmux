import { describe, expect, it } from 'bun:test'
import { newModulePath } from './go-module-path.mjs'

function upgrade(extra = {}) {
  return {
    depName: 'example.com/library/v7',
    newValue: 'v8.1.0',
    ...extra,
  }
}

describe('newModulePath', () => {
  it('handles version suffixes, first-major paths, and incompatible modules', () => {
    expect(newModulePath(upgrade())).toBe('example.com/library/v8')
    expect(newModulePath(upgrade({ depName: 'gopkg.in/yaml.v2', newValue: 'v3.0.1' }))).toBe('gopkg.in/yaml.v3')
    expect(newModulePath(upgrade({ depName: 'gopkg.in/tomb.v2-unstable', newValue: 'v3.0.0' }))).toBe('gopkg.in/tomb.v3')
    expect(newModulePath(upgrade({ depName: 'example.com/library', newValue: 'v2.0.0' }))).toBe('example.com/library/v2')
    expect(newModulePath(upgrade({ depName: 'example.com/library', newValue: 'v2.0.0+incompatible' }))).toBe('example.com/library')
    expect(newModulePath(upgrade({ depName: 'google.golang.org/genproto', newValue: 'v0.0.0-20261020000000-abcdefabcdef' }))).toBe('google.golang.org/genproto')
  })
  it('rejects a version without a numeric major', () => {
    for (const newValue of ['8.1.0', 'vX.1.0', ''])
      expect(() => newModulePath(upgrade({ newValue })), newValue).toThrow('Invalid Go module version')
  })
})
