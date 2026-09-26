import { existsSync, mkdirSync, mkdtempSync, readFileSync, rmSync } from 'node:fs'
import { basename, join, resolve } from 'node:path'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { writeAttachmentFixture } from './attachments'

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
    ['pdf', 'doc.pdf', '%PDF-1.1'],
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

  it('writes a PDF header the MIME sniffer reads', () => {
    expect(readFileSync(writeAttachmentFixture('pdf', 'x.pdf'), 'utf8').startsWith('%PDF-')).toBe(true)
  })
})
