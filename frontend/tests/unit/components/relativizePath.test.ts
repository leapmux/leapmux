import { describe, expect, it } from 'vitest'
import { relativizePath, tildify } from '~/components/chat/messageRenderers'

describe('relativizePath', () => {
  // --- Existing behavior (no homeDir) ---

  it('returns absPath when no workingDir', () => {
    expect(relativizePath('/home/user/foo')).toBe('/home/user/foo')
  })

  it('strips common prefix for paths under workingDir', () => {
    expect(relativizePath('/home/user/project/src/foo.ts', '/home/user/project'))
      .toBe('src/foo.ts')
  })

  it('handles workingDir with trailing slash', () => {
    expect(relativizePath('/home/user/project/src/foo.ts', '/home/user/project/'))
      .toBe('src/foo.ts')
  })

  it('produces ../ relative paths', () => {
    expect(relativizePath('/home/user/other/file.ts', '/home/user/project'))
      .toBe('../other/file.ts')
  })

  it('returns absolute path when relative is longer', () => {
    // A path that shares no common prefix results in a long ../ chain
    expect(relativizePath('/a', '/very/deeply/nested/working/directory'))
      .toBe('/a')
  })

  // --- New tilde behavior ---

  it('uses ~/... when shorter than ../ form', () => {
    expect(relativizePath('/home/user/foo', '/home/user/bar/baz/qux', '/home/user'))
      .toBe('~/foo')
  })

  it('prefers direct relative over tilde when path is under workingDir', () => {
    expect(relativizePath('/home/user/project/src/foo.ts', '/home/user/project', '/home/user'))
      .toBe('src/foo.ts')
  })

  it('uses ~ for exact home directory', () => {
    expect(relativizePath('/home/user', '/some/other/dir', '/home/user'))
      .toBe('~')
  })

  it('falls back to absolute when path is not under home', () => {
    expect(relativizePath('/opt/data/file.txt', '/home/user/project', '/home/user'))
      .toBe('/opt/data/file.txt')
  })

  it('handles homeDir with trailing slash', () => {
    expect(relativizePath('/home/user/foo', '/home/user/bar/baz/qux', '/home/user/'))
      .toBe('~/foo')
  })

  it('prefers ../ when shorter than tilde', () => {
    // ../foo (6 chars) vs ~/project/foo (14 chars) -- ../ wins
    expect(relativizePath('/home/user/project/foo', '/home/user/project/bar', '/home/user'))
      .toBe('../foo')
  })

  it('handles undefined homeDir gracefully', () => {
    expect(relativizePath('/home/user/foo', '/home/user/bar/baz/qux', undefined))
      .toBe('../../../foo')
  })

  it('handles empty homeDir gracefully', () => {
    expect(relativizePath('/home/user/foo', '/home/user/bar/baz/qux', ''))
      .toBe('../../../foo')
  })
})

describe('tildify', () => {
  it('returns absPath when no homeDir', () => {
    expect(tildify('/home/user/project')).toBe('/home/user/project')
    expect(tildify('/home/user/project', undefined)).toBe('/home/user/project')
    expect(tildify('/home/user/project', '')).toBe('/home/user/project')
  })

  it('returns ~ for exact home directory', () => {
    expect(tildify('/home/user', '/home/user')).toBe('~')
  })

  it('returns ~/... for sub-path under home', () => {
    expect(tildify('/home/user/project', '/home/user')).toBe('~/project')
    expect(tildify('/home/user/a/b/c', '/home/user')).toBe('~/a/b/c')
  })

  it('returns absPath when path is not under home', () => {
    expect(tildify('/opt/data/file.txt', '/home/user')).toBe('/opt/data/file.txt')
  })

  it('handles homeDir with trailing slash', () => {
    expect(tildify('/home/user/project', '/home/user/')).toBe('~/project')
    expect(tildify('/home/user', '/home/user/')).toBe('~')
  })
})
