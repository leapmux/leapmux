import { Buffer } from 'node:buffer'
import { existsSync, mkdirSync, mkdtempSync, readdirSync, readFileSync, rmSync, writeFileSync } from 'node:fs'
import { basename, join, resolve } from 'node:path'
import { crc32, inflateSync } from 'node:zlib'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { selectAttachmentFixture, writeAttachmentFixture } from './attachments'

let runDir: string
vi.mock('./server', () => ({ getGlobalState: () => ({ tmpDir: runDir }) }))

beforeEach(() => {
  const scratch = resolve(import.meta.dirname, '../../../..', '.tmp')
  mkdirSync(scratch, { recursive: true })
  runDir = mkdtempSync(join(scratch, 'attachment-fixtures-'))
})

afterEach(() => rmSync(runDir, { recursive: true, force: true }))

describe('writeAttachmentFixture', () => {
  it.each([
    ['text', 'notes.txt', 'leapmux attachment text fixture'],
    ['image', 'shot.png', ''],
    ['pdf', 'doc.pdf', '%PDF-1.4'],
    ['binary', 'blob.bin', ''],
  ] as const)('writes a %s fixture under the run directory', (kind, name, prefix) => {
    const path = writeAttachmentFixture(kind, name)
    expect(basename(path)).toBe(name)
    expect(existsSync(path)).toBe(true)
    if (prefix)
      expect(readFileSync(path, 'utf8').startsWith(prefix)).toBe(true)
  })

  it('fills a name when the caller gives none', () => {
    expect(basename(writeAttachmentFixture('image'))).toBe('shot.png')
    expect(basename(writeAttachmentFixture('pdf'))).toBe('doc.pdf')
    expect(basename(writeAttachmentFixture('binary'))).toBe('blob.bin')
    expect(basename(writeAttachmentFixture('text'))).toBe('notes.txt')
  })

  it('writes the complete text marker without a terminal newline', () => {
    expect(readFileSync(writeAttachmentFixture('text'), 'utf8')).toBe('leapmux attachment text fixture')
  })

  it('gives each call its own directory, so two fixtures never collide', () => {
    const first = writeAttachmentFixture('text', 'notes.txt')
    const second = writeAttachmentFixture('text', 'notes.txt')
    expect(first).not.toBe(second)
    expect(existsSync(first)).toBe(true)
    expect(existsSync(second)).toBe(true)
  })

  it('writes a decodable PNG for the image kind', () => {
    const bytes = readFileSync(writeAttachmentFixture('image', 'x.png'))
    // 8-byte PNG signature, then IHDR.
    expect([...bytes.subarray(0, 8)]).toEqual([0x89, 0x50, 0x4E, 0x47, 0x0D, 0x0A, 0x1A, 0x0A])
    expect(bytes.length).toBeGreaterThan(8)
  })

  it('writes a PNG with valid chunk checksums', () => {
    const bytes = readFileSync(writeAttachmentFixture('image', 'checked.png'))
    let offset = 8
    let sawEnd = false
    while (offset + 12 <= bytes.length) {
      const length = bytes.readUInt32BE(offset)
      const end = offset + 12 + length
      expect(end).toBeLessThanOrEqual(bytes.length)
      const type = bytes.toString('ascii', offset + 4, offset + 8)
      const actual = crc32(bytes.subarray(offset + 4, offset + 8 + length))
      expect(actual, `${type} checksum`).toBe(bytes.readUInt32BE(offset + 8 + length))
      offset = end
      if (type === 'IEND') {
        sawEnd = true
        break
      }
    }
    expect(sawEnd).toBe(true)
    expect(offset).toBe(bytes.length)
  })

  it('writes distinct colors into all four PNG quadrants', () => {
    const bytes = readFileSync(writeAttachmentFixture('image', 'quadrants.png'))
    expect(bytes.readUInt32BE(16)).toBe(16)
    expect(bytes.readUInt32BE(20)).toBe(16)
    expect(bytes[25]).toBe(2)

    const compressed: Buffer[] = []
    for (let offset = 8; offset + 12 <= bytes.length;) {
      const length = bytes.readUInt32BE(offset)
      const kind = bytes.toString('ascii', offset + 4, offset + 8)
      if (kind === 'IDAT')
        compressed.push(bytes.subarray(offset + 8, offset + 8 + length))
      offset += 12 + length
    }
    const pixels = inflateSync(Buffer.concat(compressed))
    const rowLength = 1 + 16 * 3
    expect(pixels).toHaveLength(16 * rowLength)
    const pixel = (x: number, y: number) => {
      expect(pixels[y * rowLength]).toBe(0)
      const start = y * rowLength + 1 + x * 3
      return [...pixels.subarray(start, start + 3)]
    }
    expect(pixel(3, 3)).toEqual([255, 0, 0])
    expect(pixel(12, 3)).toEqual([0, 255, 0])
    expect(pixel(3, 12)).toEqual([0, 0, 255])
    expect(pixel(12, 12)).toEqual([255, 255, 0])
  })

  it('writes a PDF header the MIME sniffer reads', () => {
    expect(readFileSync(writeAttachmentFixture('pdf', 'x.pdf'), 'utf8').startsWith('%PDF-')).toBe(true)
  })

  it('writes a visible PDF page with exact cross-reference offsets', () => {
    const body = readFileSync(writeAttachmentFixture('pdf', 'page.pdf'), 'utf8')
    expect(body).toContain('(LEAPMUX_PDF_PAGE_49) Tj')
    for (const color of ['1 0 0 rg', '0 1 0 rg', '0 0 1 rg', '1 1 0 rg'])
      expect(body).toContain(color)

    const start = /startxref\n(\d+)\n%%EOF\n?$/.exec(body)
    if (!start)
      throw new Error('the PDF has no startxref offset')
    const crossReference = body.slice(Number(start[1])).split('\n')
    expect(crossReference[0]).toBe('xref')
    expect(crossReference[1]).toBe('0 6')
    for (let object = 1; object <= 5; object++) {
      const entry = crossReference[object + 2]
      expect(entry).toMatch(/^\d{10} 00000 n $/)
      expect(body.slice(Number(entry?.slice(0, 10)))).toMatch(new RegExp(`^${object} 0 obj`))
    }
  })
})

describe('selectAttachmentFixture', () => {
  it('uses the caller file without creating a different fixture', () => {
    const path = join(runDir, 'large.png')
    const bytes = Buffer.from([0x89, 0x50, 0x4E, 0x47, 0x00, 0x42])
    writeFileSync(path, bytes)

    expect(selectAttachmentFixture('image', { supported: true, fileName: 'large.png', fixturePath: path })).toBe(path)
    expect(readFileSync(path)).toEqual(bytes)
    expect(readdirSync(runDir)).toEqual(['large.png'])
  })

  it('rejects a file name that differs from the caller file', () => {
    const path = join(runDir, 'large.png')
    expect(() => selectAttachmentFixture('image', { supported: true, fileName: 'other.png', fixturePath: path }))
      .toThrow('fixture path and file name must agree')
  })
})
