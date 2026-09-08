import { describe, expect, it } from 'vitest'
import {
  basename,
  detectFlavor,
  extname,
  filesystemRoot,
  flavorFromOs,
  isAbsolute,
  join,
  parentDirectory,
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

  /**
   * A POSIX root is nothing but a separator, so stripping the trailing one
   * empties it. Without the root prefix the result came out RELATIVE, and a
   * tree rooted at `/` built every git path wrong.
   */
  it('keeps the leading separator when the first fragment is a posix root', () => {
    expect(join(['/', 'home'], 'posix')).toBe('/home')
    expect(join(['/', 'home', 'alice'], 'posix')).toBe('/home/alice')
    expect(join(['/', '/home/', 'alice'], 'posix')).toBe('/home/alice')
  })

  it('answers the root itself when there is nothing to append', () => {
    expect(join(['/'], 'posix')).toBe('/')
    expect(join(['/', ''], 'posix')).toBe('/')
  })

  // The same shape on win32: `\foo` is rooted but volume-less. `C:\` is NOT
  // this case -- it strips to `C:`, which still names the drive, which is why
  // the defect only ever showed on POSIX.
  it('keeps the leading separator for a volume-less win32 root', () => {
    expect(join(['\\', 'foo'], 'win32')).toBe('\\foo')
    expect(join(['C:\\', 'Users', 'alice'], 'win32')).toBe('C:\\Users\\alice')
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

describe('extname', () => {
  it('returns the lowercase extension of a simple posix path', () => {
    expect(extname('photo.png')).toBe('png')
    expect(extname('/home/u/Photo.PNG')).toBe('png')
  })

  it('returns the lowercase extension of a Windows path', () => {
    expect(extname('C:\\Users\\u\\photo.PNG')).toBe('png')
    expect(extname('C:\\x.tar.gz')).toBe('gz')
  })

  it('returns empty for paths with no extension', () => {
    expect(extname('/etc/hosts')).toBe('')
    expect(extname('Makefile')).toBe('')
    expect(extname('C:\\Users\\u\\Dockerfile')).toBe('')
  })

  it('returns empty when the dot is inside a parent directory, not the file', () => {
    expect(extname('dir.zip/file')).toBe('')
    expect(extname('dir.zip\\file')).toBe('')
  })

  it('returns the extension when only the final segment has one', () => {
    expect(extname('archive.zip/inside/file.txt')).toBe('txt')
    expect(extname('C:\\dir.zip\\inside\\file.txt')).toBe('txt')
  })

  it('treats a leading-dot file as having an extension equal to the name', () => {
    expect(extname('.gitignore')).toBe('gitignore')
    expect(extname('/repo/.bashrc')).toBe('bashrc')
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

  // The home directory is a path prefix, not a string prefix. A naive
  // startsWith would abbreviate a sibling whose name merely begins with it,
  // and print the nonsense '~server/proj' for a real directory.
  it('leaves a sibling whose name starts with homeDir alone', () => {
    expect(tildify('/home/aliceserver/proj', '/home/alice')).toBe('/home/aliceserver/proj')
    expect(tildify('C:\\Users\\aliceserver\\proj', 'C:\\Users\\alice')).toBe('C:\\Users\\aliceserver\\proj')
  })

  // A configured home directory that carries a trailing separator must abbreviate
  // the same way, or the same worker reports two spellings for one directory.
  it('ignores a trailing separator on homeDir', () => {
    expect(tildify('/home/alice/proj', '/home/alice/')).toBe('~/proj')
    expect(tildify('C:\\Users\\alice\\proj', 'C:\\Users\\alice\\')).toBe('~\\proj')
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

  // A home directory at the filesystem root goes through `join`'s root case.
  // Without it this answered `proj` -- a RELATIVE path where the caller needs
  // an absolute one.
  it('expands against a home directory that is the filesystem root', () => {
    expect(untildify('~/proj', '/', 'posix')).toBe('/proj')
    expect(untildify('~', '/', 'posix')).toBe('/')
  })
})

describe('filesystemRoot', () => {
  it('returns / for any absolute posix path', () => {
    expect(filesystemRoot('/', 'posix')).toBe('/')
    expect(filesystemRoot('/etc/hosts', 'posix')).toBe('/')
    expect(filesystemRoot('/a/b/', 'posix')).toBe('/')
  })

  it('returns undefined for a relative posix path', () => {
    expect(filesystemRoot('proj/src', 'posix')).toBeUndefined()
    expect(filesystemRoot('', 'posix')).toBeUndefined()
  })

  it('returns the drive root with the native separator on win32', () => {
    expect(filesystemRoot('C:\\Users\\alice', 'win32')).toBe('C:\\')
    expect(filesystemRoot('C:/Users/alice', 'win32')).toBe('C:\\')
  })

  it('is idempotent on a drive root', () => {
    expect(filesystemRoot('C:\\', 'win32')).toBe('C:\\')
    expect(filesystemRoot('C:/', 'win32')).toBe('C:\\')
  })

  it('returns the share root for a UNC path', () => {
    expect(filesystemRoot('\\\\srv\\share\\x', 'win32')).toBe('\\\\srv\\share\\')
    expect(filesystemRoot('\\\\srv\\share\\', 'win32')).toBe('\\\\srv\\share\\')
  })

  // Which drive a volume-less rooted path lands on is the worker's current
  // directory, so the browser must not guess one.
  it('returns undefined for a win32 path with no volume', () => {
    expect(filesystemRoot('\\rooted', 'win32')).toBeUndefined()
    expect(filesystemRoot('/rooted', 'win32')).toBeUndefined()
    expect(filesystemRoot('proj\\src', 'win32')).toBeUndefined()
  })

  it('sniffs the flavor when none is given', () => {
    expect(filesystemRoot('C:\\x')).toBe('C:\\')
    expect(filesystemRoot('/x')).toBe('/')
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

  // A filesystem root IS a trailing separator, so without this every path
  // under a root answered null and a tree rooted at `/` found no descendants.
  it('treats a trailing separator on the base as a root', () => {
    expect(relativeUnder('/a/b', '/', 'posix')).toBe('a/b')
    expect(relativeUnder('/', '/', 'posix')).toBe('')
    expect(relativeUnder('C:\\Users', 'C:\\', 'win32')).toBe('Users')
    expect(relativeUnder('C:\\', 'C:\\', 'win32')).toBe('')
    expect(relativeUnder('\\\\srv\\share\\x', '\\\\srv\\share\\', 'win32')).toBe('x')
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

describe('relativizePath from a filesystem root', () => {
  // The picker's tree is rooted at `/` now. Without the root guard the direct
  // relative answer wins and "Copy relative path" reads `home/alice/proj/a.ts`.
  it('prefers the tilde form when the base is the filesystem root', () => {
    expect(relativizePath('/home/alice/proj/a.ts', '/', '/home/alice')).toBe('~/proj/a.ts')
  })

  it('falls back to the absolute path when the tilde does not apply', () => {
    expect(relativizePath('/opt/data', '/', '/home/alice')).toBe('/opt/data')
  })

  it('still answers . for the root itself', () => {
    expect(relativizePath('/', '/')).toBe('.')
    expect(relativizePath('C:\\', 'C:\\')).toBe('.')
  })

  it('prefers the tilde form from a win32 drive root', () => {
    expect(relativizePath('C:\\Users\\alice\\p', 'C:\\', 'C:\\Users\\alice')).toBe('~\\p')
  })

  // Regression guard for rewriting rootsMatch on top of filesystemRoot.
  it('still compares win32 volumes case-insensitively', () => {
    expect(relativizePath('C:\\proj\\src', 'c:\\PROJ')).toBe('src')
  })

  // rootsMatch is reached through relativizePath. Its rewrite must keep
  // answering false when only ONE side has a root, or a `../` chain would be
  // offered between an absolute path and a relative base.
  it('offers no relative chain between an absolute path and a relative base', () => {
    expect(relativizePath('/opt/data', 'rel/base')).toBe('/opt/data')
    expect(relativizePath('C:\\opt\\data', 'rel\\base', undefined, 'win32')).toBe('C:\\opt\\data')
  })
})
