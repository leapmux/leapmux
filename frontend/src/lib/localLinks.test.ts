import type { LocalLinkResolveOptions } from './localLinks'
import { describe, expect, it, vi } from 'vitest'
import {
  isLocalLinkHref,
  parseLocalLinkHref,
  resolveLocalLinkCandidates,
  tooltipPathForLocalLink,
} from './localLinks'

const POSIX_OPTS: LocalLinkResolveOptions = {
  baseDir: '/repo/docs',
  workingDir: '/repo',
  homeDir: '/Users/alice',
  flavor: 'posix',
}

const WIN32_OPTS: LocalLinkResolveOptions = {
  baseDir: 'C:\\repo\\docs',
  workingDir: 'C:\\repo',
  homeDir: 'C:\\Users\\alice',
  flavor: 'win32',
}

describe('parseLocalLinkHref', () => {
  it('accepts relative, POSIX absolute, Win32 drive, tilde, and local file URI paths', () => {
    expect(parseLocalLinkHref('README.md')).toEqual({
      raw: 'README.md',
      path: 'README.md',
      fromFileUri: false,
      isTilde: false,
    })
    expect(parseLocalLinkHref('./a.ts')?.path).toBe('./a.ts')
    expect(parseLocalLinkHref('../b.md')?.path).toBe('../b.md')
    expect(parseLocalLinkHref('.')?.path).toBe('.')
    expect(parseLocalLinkHref('..')?.path).toBe('..')
    expect(parseLocalLinkHref('dir/')?.path).toBe('dir/')
    expect(parseLocalLinkHref('/etc/hosts')?.path).toBe('/etc/hosts')
    expect(parseLocalLinkHref('C:\\a\\b.ts')?.path).toBe('C:\\a\\b.ts')
    expect(parseLocalLinkHref('C:%5Crepo%5Ca.png')?.path).toBe('C:\\repo\\a.png')
    expect(parseLocalLinkHref('C:/a/b.ts')?.path).toBe('C:/a/b.ts')
    expect(parseLocalLinkHref('~')).toEqual({
      raw: '~',
      path: '~',
      fromFileUri: false,
      isTilde: true,
    })
    expect(parseLocalLinkHref('~/notes.md')).toEqual({
      raw: '~/notes.md',
      path: '~/notes.md',
      fromFileUri: false,
      isTilde: true,
    })
    expect(parseLocalLinkHref('~\\notes.md')).toEqual({
      raw: '~\\notes.md',
      path: '~\\notes.md',
      fromFileUri: false,
      isTilde: true,
    })
    expect(parseLocalLinkHref('file:///tmp/a.md')).toEqual({
      raw: 'file:///tmp/a.md',
      path: '/tmp/a.md',
      fromFileUri: true,
      isTilde: false,
    })
    expect(parseLocalLinkHref('file://localhost/tmp/a.md')?.path).toBe('/tmp/a.md')
    expect(parseLocalLinkHref('FILE://LOCALHOST/tmp/a.md')?.path).toBe('/tmp/a.md')
    expect(parseLocalLinkHref('file:///C:/repo/a.md')?.path).toBe('C:/repo/a.md')
    expect(parseLocalLinkHref('file://localhost/C:/repo/a.md')?.path).toBe('C:/repo/a.md')
  })

  it('strips query and fragment and safely decodes percent-encoded paths', () => {
    expect(parseLocalLinkHref('docs/my%20file.md?raw=1#L10')?.path).toBe('docs/my file.md')
    expect(parseLocalLinkHref('a%23b.txt')?.path).toBe('a#b.txt')
    expect(parseLocalLinkHref('bad%ZZname.txt')?.path).toBe('bad%ZZname.txt')
    expect(parseLocalLinkHref('docs/My%20Report%2050%_final.md')?.path).toBe('docs/My Report 50%_final.md')
    expect(parseLocalLinkHref('C:%5Crepo%5C50%_done.png')?.path).toBe('C:\\repo\\50%_done.png')
    expect(parseLocalLinkHref('C:%5Crepo%5C50%FF_done.png')?.path).toBe('C:\\repo\\50%FF_done.png')
    expect(parseLocalLinkHref('file:///tmp/my%20doc.md?v=1#top')?.path).toBe('/tmp/my doc.md')
    expect(parseLocalLinkHref('file:///tmp/bad%ZZ.md')?.path).toBe('/tmp/bad%ZZ.md')
  })

  it('extracts pathWithoutLineSuffix for :line, :line:col, and :line-end suffixes', () => {
    expect(parseLocalLinkHref('app.ts:42')).toEqual({
      raw: 'app.ts:42',
      path: 'app.ts:42',
      pathWithoutLineSuffix: 'app.ts',
      fromFileUri: false,
      isTilde: false,
    })
    expect(parseLocalLinkHref('src/app.ts:10:5')).toEqual({
      raw: 'src/app.ts:10:5',
      path: 'src/app.ts:10:5',
      pathWithoutLineSuffix: 'src/app.ts',
      fromFileUri: false,
      isTilde: false,
    })
    expect(parseLocalLinkHref('app.ts:10-20')?.pathWithoutLineSuffix).toBe('app.ts')
    expect(parseLocalLinkHref('./Makefile:10')?.pathWithoutLineSuffix).toBe('./Makefile')
    expect(parseLocalLinkHref('src%5CMakefile:42')?.pathWithoutLineSuffix).toBe('src\\Makefile')
    expect(parseLocalLinkHref('~%5CREADME:10')?.pathWithoutLineSuffix).toBe('~\\README')
    expect(parseLocalLinkHref('C:\\repo\\app.ts:42')?.pathWithoutLineSuffix).toBe('C:\\repo\\app.ts')
  })

  it.each([
    '',
    '   ',
    'a\0b.txt',
    'a\nb.txt',
    'a\x7Fb.txt',
    'a%00b.txt',
    'file:///tmp/a%00b.txt',
    '#only',
    '?only',
    '//evil.example/x',
    '\\\\server\\share\\x',
    '/\\evil.example/share/x',
    '\\/evil.example/share/x',
    '%5c%5cevil.example%5cshare%5cx',
    '%2f%2fevil.example/share/x',
    '/%5cevil.example/share/x',
    'file://evil.example/x',
    'file:////evil.example/share/x',
    'file://localhost//evil.example/share/x',
    'file:///\\evil.example/share/x',
    'file://localhost/\\evil.example/share/x',
    'file:///%5c%5cevil.example/share/x',
    'file:///%2f%2fevil.example/share/x',
    'file:///%5cevil.example/share/x',
    'file:///%2fevil.example/share/x',
    'file:relative',
    'file:///C:',
    'file:///C:relative.txt',
    'file://localhost/C:relative.txt',
    'file:///%43:relative.txt',
    'C:',
    'C:relative.txt',
    '%43:relative.txt',
    'javascript:alert(1)',
    'vbscript:x',
    'data:text/html,x',
    'blob:https://a/b',
    'mailto:a@b',
    'tel:+1555',
    'tel:12345',
    'http://a',
    'https://a',
    'HttPs://a',
    'artifact:0',
    'localhost:3000',
    'javascript%3aalert(1)',
    'file%3a//evil.example/share/a',
    'https%3a//evil.example/x.png',
    'x://evil.example/a',
    'x:%2f%2fevil.example/a',
    'example.com:8080/x',
    'example.com:8080/x.png',
  ])('rejects non-local, malformed, and dangerous href: %j', (href) => {
    expect(parseLocalLinkHref(href)).toBeNull()
    expect(isLocalLinkHref(href)).toBe(false)
  })

  it('rejects file://// UNC URIs even when URL collapses leading slashes like Chromium', () => {
    const NativeURL = globalThis.URL
    class ChromiumCollapsingURL extends NativeURL {
      override get pathname(): string {
        return super.pathname.replace(/^\/+/, '/')
      }
    }
    vi.stubGlobal('URL', ChromiumCollapsingURL)
    try {
      expect(parseLocalLinkHref('file:////evil.example/share/a.txt')).toBeNull()
      expect(parseLocalLinkHref('file://localhost//evil.example/share/a.txt')).toBeNull()
      expect(parseLocalLinkHref('file:///\\evil.example/share/a.txt')).toBeNull()
      expect(parseLocalLinkHref('file:///%2fevil.example/share/a.txt')).toBeNull()
    }
    finally {
      vi.unstubAllGlobals()
    }
  })
})

describe('resolveLocalLinkCandidates', () => {
  it('tries exact path before line-stripped path on posix', () => {
    expect(resolveLocalLinkCandidates('app.ts:42', POSIX_OPTS)).toEqual([
      '/repo/docs/app.ts:42',
      '/repo/docs/app.ts',
    ])
  })

  it('tries line-stripped path before exact path on win32', () => {
    expect(resolveLocalLinkCandidates('app.ts:42', WIN32_OPTS)).toEqual([
      'C:\\repo\\docs\\app.ts',
      'C:\\repo\\docs\\app.ts:42',
    ])
  })

  it('adds a repo-root fallback for non-file root-relative paths on posix', () => {
    expect(resolveLocalLinkCandidates('/LICENSE.md', POSIX_OPTS)).toEqual([
      '/LICENSE.md',
      '/repo/LICENSE.md',
    ])
    expect(resolveLocalLinkCandidates('/LICENSE.md', { ...POSIX_OPTS, workingDir: '/' })).toEqual([
      '/LICENSE.md',
    ])
    expect(resolveLocalLinkCandidates('file:///LICENSE.md', POSIX_OPTS)).toEqual([
      '/LICENSE.md',
    ])
  })

  it('tries repo-root before volume-rooted for non-file leading-slash paths on win32', () => {
    expect(resolveLocalLinkCandidates('/LICENSE.md', WIN32_OPTS)).toEqual([
      'C:\\repo\\LICENSE.md',
      'C:\\LICENSE.md',
    ])
    expect(resolveLocalLinkCandidates('/foo', {
      baseDir: '',
      workingDir: '',
      homeDir: '',
      flavor: 'win32',
    })).toEqual([])
  })

  it('expands tilde paths with dot-dot cleaning and skips them when homeDir is empty', () => {
    expect(resolveLocalLinkCandidates('~/../shared/doc.md', POSIX_OPTS)).toEqual([
      '/Users/shared/doc.md',
    ])
    expect(resolveLocalLinkCandidates('~/notes.md', { ...POSIX_OPTS, homeDir: '' })).toEqual([])
    // Backslash is a literal filename character on POSIX, not a tilde separator.
    expect(resolveLocalLinkCandidates('~\\notes.md', POSIX_OPTS)).toEqual([
      '/repo/docs/~\\notes.md',
    ])
    expect(resolveLocalLinkCandidates('~\\notes.md', { ...POSIX_OPTS, homeDir: '' })).toEqual([
      '/repo/docs/~\\notes.md',
    ])
    expect(resolveLocalLinkCandidates('~\\notes.md', WIN32_OPTS)).toEqual([
      'C:\\Users\\alice\\notes.md',
    ])
  })

  it('never resolves an absolute file:///C:/... URI relative to baseDir on posix', () => {
    expect(resolveLocalLinkCandidates('file:///C:/repo/a.md', POSIX_OPTS)).toEqual([
      '/C:/repo/a.md',
    ])
    expect(resolveLocalLinkCandidates('file:///C:/repo/a.md', WIN32_OPTS)).toEqual([
      'C:\\repo\\a.md',
    ])
  })

  it('returns empty for relative paths when baseDir and workingDir are both empty', () => {
    expect(resolveLocalLinkCandidates('./guide.md', {
      baseDir: '',
      workingDir: '',
      homeDir: '/Users/alice',
      flavor: 'posix',
    })).toEqual([])
  })

  it('never emits a UNC candidate even if baseDir or workingDir is a UNC path', () => {
    expect(resolveLocalLinkCandidates('./secret.txt', {
      baseDir: '\\\\evil\\share\\repo',
      workingDir: '\\\\evil\\share\\repo',
      homeDir: '',
      flavor: 'win32',
    })).toEqual([])
  })
})

describe('tooltipPathForLocalLink', () => {
  it('returns the primary resolved absolute path and prefers the line-stripped path for file:line links', () => {
    expect(tooltipPathForLocalLink('../README.md', POSIX_OPTS)).toBe('/repo/README.md')
    expect(tooltipPathForLocalLink('app.ts:42', POSIX_OPTS)).toBe('/repo/docs/app.ts')
    expect(tooltipPathForLocalLink('src/app.ts:10:5', WIN32_OPTS)).toBe('C:\\repo\\docs\\src\\app.ts')
    expect(tooltipPathForLocalLink('javascript:alert(1)', POSIX_OPTS)).toBeUndefined()
    expect(tooltipPathForLocalLink('./a.md', {
      baseDir: '',
      workingDir: '',
      homeDir: '',
      flavor: 'posix',
    })).toBeUndefined()
  })
})
