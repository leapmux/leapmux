import { describe, expect, it } from 'vitest'
import {
  basename,
  detectFlavor,
  flavorFromOs,
  isAbsolute,
  join,
  parentDirectory,
  pathSegments,
  relativeUnder,
  relativizePath,
  split,
  tildify,
  toPosixSeparators,
  untildify,
} from './paths'

describe('detectFlavor', () => {
  it('recognizes drive-letter paths as win32', () => {
    expect(detectFlavor('C:\\Users\\u')).toBe('win32')
    expect(detectFlavor('c:\\Users\\u')).toBe('win32')
    expect(detectFlavor('C:/Users/u')).toBe('win32')
  })

  it('recognizes UNC paths as win32', () => {
    expect(detectFlavor('\\\\srv\\share\\foo')).toBe('win32')
    expect(detectFlavor('\\\\?\\C:\\x')).toBe('win32')
  })

  it('recognizes rooted-no-drive paths as win32', () => {
    expect(detectFlavor('\\foo')).toBe('win32')
  })

  it('treats POSIX and relative paths as posix', () => {
    expect(detectFlavor('/home/alice')).toBe('posix')
    expect(detectFlavor('./rel')).toBe('posix')
    expect(detectFlavor('rel/path')).toBe('posix')
  })

  it('returns posix for empty input', () => {
    expect(detectFlavor('')).toBe('posix')
  })
})

describe('flavorFromOs', () => {
  it('maps windows to win32', () => {
    expect(flavorFromOs('windows')).toBe('win32')
    expect(flavorFromOs('Windows')).toBe('win32')
  })

  it('maps everything else to posix', () => {
    expect(flavorFromOs('linux')).toBe('posix')
    expect(flavorFromOs('darwin')).toBe('posix')
    expect(flavorFromOs(undefined)).toBe('posix')
    expect(flavorFromOs('')).toBe('posix')
  })
})

describe('isAbsolute', () => {
  it('posix paths are absolute iff they start with /', () => {
    expect(isAbsolute('/home/u')).toBe(true)
    expect(isAbsolute('home/u')).toBe(false)
    expect(isAbsolute('./rel')).toBe(false)
  })

  it('win32 drive-letter, UNC, and rooted paths are absolute', () => {
    expect(isAbsolute('C:\\foo', 'win32')).toBe(true)
    expect(isAbsolute('C:/foo', 'win32')).toBe(true)
    expect(isAbsolute('\\\\srv\\share', 'win32')).toBe(true)
    expect(isAbsolute('\\foo', 'win32')).toBe(true)
    expect(isAbsolute('/foo', 'win32')).toBe(true)
    expect(isAbsolute('rel', 'win32')).toBe(false)
  })
})

describe('split', () => {
  it('splits a posix path into non-empty segments', () => {
    expect(split('/home/alice/foo/')).toEqual(['home', 'alice', 'foo'])
    expect(split('/')).toEqual([])
  })

  it('extracts a drive-letter volume as the first segment', () => {
    expect(split('C:\\Users\\alice')).toEqual(['C:', 'Users', 'alice'])
    expect(split('C:/Users/alice')).toEqual(['C:', 'Users', 'alice'])
  })

  it('extracts a UNC root as the first segment', () => {
    expect(split('\\\\srv\\share\\foo\\bar')).toEqual(['\\\\srv\\share', 'foo', 'bar'])
  })

  it('handles mixed separators', () => {
    expect(split('C:\\Users/alice\\proj')).toEqual(['C:', 'Users', 'alice', 'proj'])
  })

  it('returns empty for empty input', () => {
    expect(split('')).toEqual([])
  })
})

describe('pathSegments', () => {
  it('builds cumulative POSIX segments', () => {
    expect(pathSegments('/home/alice/foo')).toEqual([
      { name: 'home', path: '/home' },
      { name: 'alice', path: '/home/alice' },
      { name: 'foo', path: '/home/alice/foo' },
    ])
  })

  it('builds cumulative Win32 segments starting at the volume root', () => {
    expect(pathSegments('C:\\Users\\alice')).toEqual([
      { name: 'C:\\', path: 'C:\\' },
      { name: 'Users', path: 'C:\\Users' },
      { name: 'alice', path: 'C:\\Users\\alice' },
    ])
  })

  it('handles UNC roots', () => {
    expect(pathSegments('\\\\srv\\share\\foo')).toEqual([
      { name: '\\\\srv\\share\\', path: '\\\\srv\\share\\' },
      { name: 'foo', path: '\\\\srv\\share\\foo' },
    ])
  })

  it('returns empty for root-only inputs', () => {
    expect(pathSegments('/')).toEqual([])
  })
})

describe('join', () => {
  it('joins win32 fragments with backslashes', () => {
    expect(join(['C:\\', 'a', 'b'], 'win32')).toBe('C:\\a\\b')
    expect(join(['C:', 'a', 'b'], 'win32')).toBe('C:\\a\\b')
  })

  it('joins posix fragments with forward slashes', () => {
    expect(join(['/home', 'alice', 'proj'], 'posix')).toBe('/home/alice/proj')
  })

  it('infers flavor from the first fragment when flavor is omitted', () => {
    expect(join(['C:\\', 'a', 'b'])).toBe('C:\\a\\b')
    expect(join(['/home', 'alice'])).toBe('/home/alice')
  })

  it('normalizes forward slashes to backslashes on win32', () => {
    expect(join(['C:/', 'a', 'b'], 'win32')).toBe('C:\\a\\b')
  })

  it('drops empty and consecutive separators', () => {
    expect(join(['/home/', '/alice/', '/proj'], 'posix')).toBe('/home/alice/proj')
  })
})

describe('parentDirectory', () => {
  it('walks up posix paths', () => {
    expect(parentDirectory('/home/alice/proj')).toBe('/home/alice')
    expect(parentDirectory('/home')).toBe('/')
    expect(parentDirectory('/')).toBe('/')
  })

  it('walks up win32 paths', () => {
    expect(parentDirectory('C:\\Users\\alice\\proj')).toBe('C:\\Users\\alice')
    expect(parentDirectory('C:\\Users')).toBe('C:\\')
    expect(parentDirectory('C:\\')).toBe('C:\\')
  })
})

describe('basename', () => {
  it('returns the last component on posix', () => {
    expect(basename('/home/alice/foo.txt')).toBe('foo.txt')
  })

  it('returns the last component on win32', () => {
    expect(basename('C:\\Users\\alice\\foo.txt')).toBe('foo.txt')
  })

  it('returns empty for roots', () => {
    expect(basename('/')).toBe('')
  })
})

describe('tildify', () => {
  it('replaces the posix home prefix', () => {
    expect(tildify('/home/alice/proj', '/home/alice')).toBe('~/proj')
    expect(tildify('/home/alice', '/home/alice')).toBe('~')
  })

  it('replaces the win32 home prefix with backslashes', () => {
    expect(tildify('C:\\Users\\alice\\proj', 'C:\\Users\\alice')).toBe('~\\proj')
  })

  it('matches win32 home prefix case-insensitively', () => {
    expect(tildify('C:\\Users\\Alice\\proj', 'c:\\users\\alice')).toBe('~\\proj')
  })

  it('leaves the path alone when homeDir does not match', () => {
    expect(tildify('/opt/data', '/home/alice')).toBe('/opt/data')
  })

  it('is a no-op when homeDir is omitted', () => {
    expect(tildify('/home/alice/proj')).toBe('/home/alice/proj')
  })
})

describe('untildify', () => {
  it('expands ~ alone to homeDir', () => {
    expect(untildify('~', '/home/alice')).toBe('/home/alice')
  })

  it('expands posix ~/sub against homeDir', () => {
    expect(untildify('~/proj', '/home/alice')).toBe('/home/alice/proj')
  })

  it('expands win32 ~\\sub against homeDir', () => {
    expect(untildify('~\\proj', 'C:\\Users\\alice', 'win32')).toBe('C:\\Users\\alice\\proj')
  })

  it('leaves non-tilde inputs alone', () => {
    expect(untildify('/opt/data', '/home/alice')).toBe('/opt/data')
    expect(untildify('relative', '/home/alice')).toBe('relative')
  })

  it('is a no-op when homeDir is missing', () => {
    expect(untildify('~/proj')).toBe('~/proj')
  })
})

describe('relativeUnder', () => {
  it('returns empty string when the paths are equal on posix', () => {
    expect(relativeUnder('/home/alice', '/home/alice', 'posix')).toBe('')
  })

  it('returns the remainder when abs is strictly under base on posix', () => {
    expect(relativeUnder('/home/alice/proj', '/home/alice', 'posix')).toBe('proj')
    expect(relativeUnder('/home/alice/proj/src', '/home/alice', 'posix')).toBe('proj/src')
  })

  it('returns null when abs is not under base on posix', () => {
    expect(relativeUnder('/home/bob', '/home/alice', 'posix')).toBeNull()
    expect(relativeUnder('/opt/data', '/home/alice', 'posix')).toBeNull()
  })

  it('distinguishes similarly-named siblings (no partial-match leakage)', () => {
    // "alice-fork" starts with "alice" but is not under it.
    expect(relativeUnder('/home/alice-fork', '/home/alice', 'posix')).toBeNull()
  })

  it('compares case-insensitively on win32', () => {
    expect(relativeUnder('C:\\Repo\\src', 'c:\\repo', 'win32')).toBe('src')
    expect(relativeUnder('C:\\Repo', 'c:\\REPO', 'win32')).toBe('')
  })

  it('returns null when the win32 volume differs', () => {
    expect(relativeUnder('D:\\data', 'C:\\data', 'win32')).toBeNull()
  })
})

describe('toPosixSeparators', () => {
  it('converts backslashes to forward slashes', () => {
    expect(toPosixSeparators('C:\\Users\\alice')).toBe('C:/Users/alice')
    expect(toPosixSeparators('a\\b\\c')).toBe('a/b/c')
  })

  it('is a no-op on posix-separated paths', () => {
    expect(toPosixSeparators('/home/alice/proj')).toBe('/home/alice/proj')
    expect(toPosixSeparators('')).toBe('')
  })

  it('leaves forward slashes untouched in mixed input', () => {
    expect(toPosixSeparators('C:/Users\\alice')).toBe('C:/Users/alice')
  })
})

describe('relativizePath', () => {
  it('returns . when the path equals the working directory', () => {
    expect(relativizePath('/home/alice', '/home/alice')).toBe('.')
  })

  it('returns the sub-path when under the working directory on posix', () => {
    expect(relativizePath('/home/alice/proj/src', '/home/alice/proj')).toBe('src')
  })

  it('returns the sub-path when under the working directory on win32', () => {
    expect(relativizePath('C:\\proj\\src', 'C:\\proj')).toBe('src')
  })

  it('picks the shortest of direct / .. / tilde on posix', () => {
    expect(relativizePath('/home/alice/docs', '/home/alice/proj', '/home/alice')).toBe('~/docs')
  })

  it('falls back to absolute when roots differ on win32', () => {
    expect(relativizePath('D:\\data\\a.txt', 'C:\\proj')).toBe('D:\\data\\a.txt')
  })
})
