import type { Page } from '@playwright/test'
import { Buffer } from 'node:buffer'
import { existsSync, mkdirSync, mkdtempSync, readdirSync, readFileSync, rmSync, writeFileSync } from 'node:fs'
import { basename, join, resolve } from 'node:path'
import { crc32, inflateSync } from 'node:zlib'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { fakeLocator } from '~/test-support/fakeLocator'
import { expectAttachmentOutcome, selectAttachmentFixture, sendWithAttachment, writeAttachmentFixture, writeAudioAttachmentFixture, writeVideoAttachmentFixture } from './attachments'

let runDir: string
vi.mock('./server', () => ({ getGlobalState: () => ({ tmpDir: runDir }) }))

const ui = vi.hoisted(() => ({
  events: [] as string[],
  hydration: vi.fn<() => Promise<void>>(),
  send: vi.fn<(page: unknown, text: string) => Promise<void>>(),
}))
vi.mock('./ui', () => ({
  sendMessage: ui.send,
  waitForNativeSettingsHydrated: ui.hydration,
}))
const toasts = vi.hoisted(() => ({ recorded: vi.fn<(page: unknown, text: string | RegExp) => Promise<void>>() }))
vi.mock('./toast', () => ({ expectToastRecorded: toasts.recorded }))

beforeEach(() => {
  const scratch = resolve(import.meta.dirname, '../../../..', '.tmp')
  mkdirSync(scratch, { recursive: true })
  runDir = mkdtempSync(join(scratch, 'attachment-fixtures-'))
  ui.events.length = 0
  ui.hydration.mockReset().mockImplementation(async () => {
    ui.events.push('hydrated')
  })
  ui.send.mockReset()
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

describe('expectAttachmentOutcome', () => {
  /** A page whose file input records the attached file, then stops the outcome before its browser assertions. */
  function attachingPage(stop: Error): Page {
    const input = {
      setInputFiles: async (path: string) => {
        ui.events.push(`attach:${basename(path)}`)
        throw stop
      },
    }
    return { locator: () => input } as unknown as Page
  }

  it.each([true, false])('waits for the live native settings catalog before it attaches the file (supported: %s)', async (supported) => {
    const stop = new Error('The fake composer stops after the attach.')
    await expect(expectAttachmentOutcome(attachingPage(stop), 'text', { supported, fileName: 'notes.txt' })).rejects.toBe(stop)
    expect(ui.hydration).toHaveBeenCalledTimes(1)
    expect(ui.events).toEqual(['hydrated', 'attach:notes.txt'])
  })

  it('requires no pill and a recorded refusal toast that names the kind, for a refused kind', async () => {
    const pills = fakeLocator(check => check.expression === 'to.have.count' && check.expectedNumber === 0)
    const visible = vi.fn()
    const page = {
      locator: (selector: string) => {
        if (selector === '[data-testid="file-input"]')
          return { setInputFiles: async () => { ui.events.push('attach') } }
        if (selector === '[data-testid="attachment-pill"]:visible')
          return pills
        visible(selector)
        throw new Error(`The refused outcome must not read ${selector}.`)
      },
    } as unknown as Page
    toasts.recorded.mockResolvedValue(undefined)
    await expectAttachmentOutcome(page, 'pdf', { supported: false })
    expect(toasts.recorded).toHaveBeenCalledExactlyOnceWith(page, /pdf/i)
    expect(visible).not.toHaveBeenCalled()
  })

  it('writes and attaches no file when the native settings catalog never hydrates', async () => {
    const failure = new Error('The live native catalog has no group with an option.')
    ui.hydration.mockRejectedValueOnce(failure)
    await expect(expectAttachmentOutcome(attachingPage(new Error('unreachable')), 'pdf', { supported: false })).rejects.toBe(failure)
    expect(ui.events).toEqual([])
    expect(readdirSync(runDir)).toEqual([])
  })
})

describe('sendWithAttachment', () => {
  /** A page whose pill locator must not be read: the send fails before the pill check, or the test stops there. */
  const page = {
    locator: () => {
      throw new Error('the send reached the pill check')
    },
  } as unknown as Page

  it('sends the text through sendMessage before it checks the pills', async () => {
    const stop = new Error('The fake send stops before the pill check.')
    ui.send.mockRejectedValueOnce(stop)
    await expect(sendWithAttachment(page, 'Inspect the attached file.')).rejects.toBe(stop)
    expect(ui.send).toHaveBeenCalledExactlyOnceWith(page, 'Inspect the attached file.')
  })

  it('checks that the pills clear after the send', async () => {
    await expect(sendWithAttachment(page, 'Inspect the attached file.')).rejects.toThrow('the send reached the pill check')
    expect(ui.send).toHaveBeenCalledOnce()
  })

  it('refuses an empty text before it sends anything', async () => {
    await expect(sendWithAttachment(page, '')).rejects.toThrow('An attachment send needs text')
    expect(ui.send).not.toHaveBeenCalled()
  })
})

describe('writeAudioAttachmentFixture', () => {
  // Reflect exercises the JavaScript boundary, including invalid format values.
  const write = (...args: unknown[]): string => Reflect.apply(writeAudioAttachmentFixture, undefined, args)

  function frameCRC(bytes: Buffer): number {
    let crc = 0
    for (const byte of bytes) {
      crc ^= byte << 8
      for (let bit = 0; bit < 8; bit++)
        crc = ((crc << 1) ^ (crc & 0x8000 ? 0x8005 : 0)) & 0xFFFF
    }
    return crc
  }

  it('writes a complete FLAC stream with audio frames and valid checksums', () => {
    const path = write('tone.flac', 'flac')
    const bytes = readFileSync(path)
    expect(basename(path)).toBe('tone.flac')
    expect(bytes.toString('ascii', 0, 4)).toBe('fLaC')
    expect(bytes[4]! & 0x7F).toBe(0)
    expect(bytes.readUIntBE(5, 3)).toBe(34)
    const stream = bytes.readBigUInt64BE(18)
    expect(Number(stream >> 44n)).toBe(44100)
    expect(Number((stream >> 41n) & 7n) + 1).toBe(1)
    expect(Number((stream >> 36n) & 31n) + 1).toBe(16)
    expect(Number(stream & 0xFFFFFFFFFn)).toBe(49152)
    let offset = 4
    for (;;) {
      const last = (bytes[offset]! & 0x80) !== 0
      offset += 4 + bytes.readUIntBE(offset + 1, 3)
      expect(offset).toBeLessThan(bytes.length)
      if (last)
        break
    }
    const frames = bytes.subarray(offset)
    expect(frames[0]).toBe(0xFF)
    expect(frames[1]! & 0xFC).toBe(0xF8)
    expect(frames.length).toBeGreaterThan(16)
    expect(frameCRC(frames)).toBe(0)
    const corrupted = Buffer.from(frames)
    corrupted[0] = 0
    expect(frameCRC(corrupted)).not.toBe(0)
  })

  it('chooses a matching default FLAC name and keeps writes in distinct directories', () => {
    const first = write(undefined, 'flac')
    const second = write('VOICE.FLAC', 'flac')
    expect(basename(first)).toBe('voice.flac')
    expect(basename(second)).toBe('VOICE.FLAC')
    expect(first).not.toBe(second)
    expect(readFileSync(first)).toEqual(readFileSync(second))
  })

  it.each([
    ['tone.wav', 'flac'],
    ['tone.flac', 'wav'],
    ['nested/tone.flac', 'flac'],
    ['', 'flac'],
    [null, 'flac'],
    ['tone.wav', 'aac'],
    ['tone.wav', null],
  ])('refuses mismatched or invalid audio names and formats: %j/%j', (name, format) => {
    const reason = format === 'wav' || format === 'flac'
      ? `The audio fixture requires a ${format.toUpperCase()} filename without a directory.`
      : 'The audio fixture format must be WAV or FLAC.'
    expect(() => write(name, format)).toThrow(reason)
  })

  it('writes a complete WAV container and its silent sample payload', () => {
    const path = writeAudioAttachmentFixture()
    const bytes = readFileSync(path)
    expect(basename(path)).toBe('voice.wav')
    expect(bytes.toString('ascii', 0, 4)).toBe('RIFF')
    expect(bytes.readUInt32LE(4)).toBe(bytes.length - 8)
    expect(bytes.toString('ascii', 8, 16)).toBe('WAVEfmt ')
    expect(bytes.readUInt16LE(20)).toBe(1)
    expect(bytes.readUInt16LE(22)).toBe(1)
    expect(bytes.readUInt32LE(24)).toBe(8000)
    expect(bytes.readUInt16LE(34)).toBe(16)
    expect(bytes.toString('ascii', 36, 40)).toBe('data')
    expect(bytes.readUInt32LE(40)).toBe(320)
    expect(bytes.subarray(44)).toEqual(Buffer.alloc(320))
  })

  it.each(['', '..', '../escape.wav', 'voice.mp3'])('refuses an invalid WAV filename: %j', (name) => {
    expect(() => writeAudioAttachmentFixture(name)).toThrow('WAV filename')
  })
})

describe('writeVideoAttachmentFixture', () => {
  function boxes(bytes: Buffer): Map<string, Buffer> {
    const found = new Map<string, Buffer>()
    let offset = 0
    while (offset < bytes.length) {
      expect(bytes.length - offset).toBeGreaterThanOrEqual(8)
      let length = bytes.readUInt32BE(offset)
      let header = 8
      if (length === 1) {
        expect(bytes.length - offset).toBeGreaterThanOrEqual(16)
        const extended = bytes.readBigUInt64BE(offset + 8)
        expect(extended).toBeLessThanOrEqual(BigInt(bytes.length))
        length = Number(extended)
        header = 16
      }
      else if (length === 0) {
        length = bytes.length - offset
      }
      expect(length).toBeGreaterThanOrEqual(header)
      expect(offset + length).toBeLessThanOrEqual(bytes.length)
      const type = bytes.toString('ascii', offset + 4, offset + 8)
      found.set(type, bytes.subarray(offset + header, offset + length))
      offset += length
    }
    expect(offset).toBe(bytes.length)
    return found
  }
  function requiredBox(parent: Map<string, Buffer>, type: string): Buffer {
    const box = parent.get(type)
    if (!box)
      throw new Error(`The video fixture has no ${type} box.`)
    return box
  }
  it('writes a complete MP4 with a video track and two encoded frames', () => {
    const path = writeVideoAttachmentFixture()
    expect(basename(path)).toBe('clip.mp4')
    const root = boxes(readFileSync(path))
    expect(requiredBox(root, 'ftyp').toString('ascii')).toContain('mp4')
    const track = boxes(requiredBox(boxes(requiredBox(root, 'moov')), 'trak'))
    const header = requiredBox(track, 'tkhd')
    expect(header.readUInt32BE(header.length - 8) / 65536).toBe(16)
    expect(header.readUInt32BE(header.length - 4) / 65536).toBe(16)
    const media = boxes(requiredBox(track, 'mdia'))
    expect(requiredBox(media, 'hdlr').toString('ascii', 8, 12)).toBe('vide')
    const table = boxes(requiredBox(boxes(requiredBox(media, 'minf')), 'stbl'))
    const sizes = requiredBox(table, 'stsz')
    expect(sizes.readUInt32BE(8)).toBe(2)
    const frameSizes = sizes.readUInt32BE(4) === 0 ? [sizes.readUInt32BE(12), sizes.readUInt32BE(16)] : [sizes.readUInt32BE(4), sizes.readUInt32BE(4)]
    for (const size of frameSizes)
      expect(size).toBeGreaterThan(0)
    expect(frameSizes.reduce((sum, size) => sum + size, 0)).toBe(requiredBox(root, 'mdat').length)
  })
  it.each(['', '..', '../escape.mp4', '/absolute.mp4', 'clip.wav', 'clip.mp4/another'])('refuses an invalid video filename: %j', (name) => {
    expect(() => writeVideoAttachmentFixture(name)).toThrow('MP4 filename')
  })
})
