import { describe, expect, it } from 'vitest'
import { detectFileViewMode, isBinaryContent, isImageExtension, isLikelyBinaryExtension, isMarkdownExtension, isSvgExtension } from '~/lib/fileType'

describe('isImageExtension', () => {
  const imageExts = ['png', 'jpg', 'jpeg', 'gif', 'bmp', 'webp', 'svg', 'ico', 'avif']
  for (const ext of imageExts) {
    it(`returns true for .${ext}`, () => {
      expect(isImageExtension(`test.${ext}`)).toBe(true)
    })
    it(`returns true for .${ext.toUpperCase()} (case-insensitive)`, () => {
      expect(isImageExtension(`test.${ext.toUpperCase()}`)).toBe(true)
    })
  }

  it('returns false for text files', () => {
    expect(isImageExtension('test.txt')).toBe(false)
    expect(isImageExtension('test.ts')).toBe(false)
    expect(isImageExtension('test.json')).toBe(false)
  })

  it('returns false for files without extension', () => {
    expect(isImageExtension('Makefile')).toBe(false)
  })

  it('handles paths with directories', () => {
    expect(isImageExtension('/home/user/images/photo.png')).toBe(true)
    expect(isImageExtension('/home/user/code/main.go')).toBe(false)
  })
})

describe('isMarkdownExtension', () => {
  it('returns true for .md', () => {
    expect(isMarkdownExtension('README.md')).toBe(true)
  })

  it('returns true for .markdown', () => {
    expect(isMarkdownExtension('doc.markdown')).toBe(true)
  })

  it('returns true for .mdx', () => {
    expect(isMarkdownExtension('page.mdx')).toBe(true)
  })

  it('is case-insensitive', () => {
    expect(isMarkdownExtension('README.MD')).toBe(true)
  })

  it('returns false for non-markdown files', () => {
    expect(isMarkdownExtension('test.txt')).toBe(false)
    expect(isMarkdownExtension('test.ts')).toBe(false)
  })
})

describe('isSvgExtension', () => {
  it('returns true for .svg', () => {
    expect(isSvgExtension('icon.svg')).toBe(true)
  })

  it('is case-insensitive', () => {
    expect(isSvgExtension('icon.SVG')).toBe(true)
  })

  it('returns false for non-svg files', () => {
    expect(isSvgExtension('icon.png')).toBe(false)
  })
})

describe('isLikelyBinaryExtension', () => {
  it('returns true for known archive extensions', () => {
    expect(isLikelyBinaryExtension('bundle.zip')).toBe(true)
    expect(isLikelyBinaryExtension('release.tar.gz')).toBe(true)
    expect(isLikelyBinaryExtension('archive.7z')).toBe(true)
  })

  it('returns true for known executable / object extensions', () => {
    expect(isLikelyBinaryExtension('app.exe')).toBe(true)
    expect(isLikelyBinaryExtension('libfoo.so')).toBe(true)
    expect(isLikelyBinaryExtension('main.o')).toBe(true)
  })

  it('returns true for office / PDF / media / font / database / disk-image extensions', () => {
    expect(isLikelyBinaryExtension('report.pdf')).toBe(true)
    expect(isLikelyBinaryExtension('slides.pptx')).toBe(true)
    expect(isLikelyBinaryExtension('song.mp3')).toBe(true)
    expect(isLikelyBinaryExtension('movie.mkv')).toBe(true)
    expect(isLikelyBinaryExtension('font.woff2')).toBe(true)
    expect(isLikelyBinaryExtension('cache.sqlite')).toBe(true)
    expect(isLikelyBinaryExtension('boot.iso')).toBe(true)
  })

  it('is case-insensitive', () => {
    expect(isLikelyBinaryExtension('Bundle.ZIP')).toBe(true)
    expect(isLikelyBinaryExtension('Report.PDF')).toBe(true)
  })

  it('returns false for source-code extensions', () => {
    expect(isLikelyBinaryExtension('index.ts')).toBe(false)
    expect(isLikelyBinaryExtension('main.go')).toBe(false)
    expect(isLikelyBinaryExtension('app.py')).toBe(false)
    expect(isLikelyBinaryExtension('README.md')).toBe(false)
  })

  it('returns false for image extensions (handled by isImageExtension)', () => {
    expect(isLikelyBinaryExtension('photo.png')).toBe(false)
    expect(isLikelyBinaryExtension('icon.svg')).toBe(false)
  })

  it('returns false for paths without an extension', () => {
    expect(isLikelyBinaryExtension('Makefile')).toBe(false)
    expect(isLikelyBinaryExtension('Dockerfile')).toBe(false)
    expect(isLikelyBinaryExtension('/etc/hosts')).toBe(false)
  })

  it('does not treat a dot inside a parent directory as an extension', () => {
    expect(isLikelyBinaryExtension('archive.zip/inside/file')).toBe(false)
    expect(isLikelyBinaryExtension('C:\\Program Files\\app\\readme')).toBe(false)
  })
})

describe('isBinaryContent', () => {
  it('returns true for content with null bytes', () => {
    const bytes = new Uint8Array([0x48, 0x65, 0x6C, 0x00, 0x6F])
    expect(isBinaryContent(bytes)).toBe(true)
  })

  it('returns false for printable ASCII text', () => {
    const text = new TextEncoder().encode('Hello, World!\n')
    expect(isBinaryContent(text)).toBe(false)
  })

  it('returns false for UTF-8 text', () => {
    const text = new TextEncoder().encode('Hello 你好 こんにちは')
    expect(isBinaryContent(text)).toBe(false)
  })

  it('returns true for high ratio of non-printable bytes', () => {
    const bytes = new Uint8Array(100)
    for (let i = 0; i < 100; i++) bytes[i] = i < 50 ? 0x01 : 0x41
    expect(isBinaryContent(bytes)).toBe(true)
  })

  it('returns false for empty content', () => {
    expect(isBinaryContent(new Uint8Array(0))).toBe(false)
  })

  it('only checks first 512 bytes', () => {
    const bytes = new Uint8Array(1024)
    bytes.fill(0x41) // Fill with 'A'
    bytes[600] = 0x00 // Null byte after 512-byte check window
    expect(isBinaryContent(bytes)).toBe(false)
  })
})

describe('detectFileViewMode', () => {
  it('returns "image" for image extensions', () => {
    expect(detectFileViewMode('photo.png', new Uint8Array([0x89, 0x50, 0x4E, 0x47]))).toBe('image')
  })

  it('returns "markdown" for markdown extensions', () => {
    const text = new TextEncoder().encode('# Hello World')
    expect(detectFileViewMode('README.md', text)).toBe('markdown')
  })

  it('returns "text" for text content', () => {
    const text = new TextEncoder().encode('function hello() {}')
    expect(detectFileViewMode('main.ts', text)).toBe('text')
  })

  it('returns "binary" for binary content with non-image extension', () => {
    const bytes = new Uint8Array([0x00, 0x01, 0x02, 0x03])
    expect(detectFileViewMode('data.bin', bytes)).toBe('binary')
  })

  it('prioritizes image extension over binary content', () => {
    const bytes = new Uint8Array([0x89, 0x50, 0x4E, 0x47, 0x00])
    expect(detectFileViewMode('image.png', bytes)).toBe('image')
  })

  it('prioritizes markdown extension over binary check', () => {
    const text = new TextEncoder().encode('# Title\n\nSome content')
    expect(detectFileViewMode('doc.md', text)).toBe('markdown')
  })
})
