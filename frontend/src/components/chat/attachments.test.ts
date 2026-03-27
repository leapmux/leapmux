import type { FileAttachment } from './attachments'
import { describe, expect, it } from 'vitest'
import {
  clearAttachments,
  getAttachments,
  isImageMimeType,
  isSupportedMimeType,
  MAX_TOTAL_ATTACHMENT_SIZE,
  nextPastedImageName,
  readFileAsAttachment,
  setAttachments,
  totalAttachmentSize,
} from './attachments'

describe('isSupportedMimeType', () => {
  it('accepts image types', () => {
    expect(isSupportedMimeType('image/png')).toBe(true)
    expect(isSupportedMimeType('image/jpeg')).toBe(true)
    expect(isSupportedMimeType('image/gif')).toBe(true)
    expect(isSupportedMimeType('image/webp')).toBe(true)
  })

  it('accepts pdf', () => {
    expect(isSupportedMimeType('application/pdf')).toBe(true)
  })

  it('rejects unsupported types', () => {
    expect(isSupportedMimeType('text/plain')).toBe(false)
    expect(isSupportedMimeType('application/json')).toBe(false)
    expect(isSupportedMimeType('video/mp4')).toBe(false)
  })
})

describe('isImageMimeType', () => {
  it('returns true for image types', () => {
    expect(isImageMimeType('image/png')).toBe(true)
    expect(isImageMimeType('image/jpeg')).toBe(true)
  })

  it('returns false for non-image types', () => {
    expect(isImageMimeType('application/pdf')).toBe(false)
    expect(isImageMimeType('text/plain')).toBe(false)
  })
})

describe('totalAttachmentSize', () => {
  it('returns 0 for empty array', () => {
    expect(totalAttachmentSize([])).toBe(0)
  })

  it('sums attachment sizes', () => {
    const attachments = [
      { size: 100 } as FileAttachment,
      { size: 200 } as FileAttachment,
      { size: 300 } as FileAttachment,
    ]
    expect(totalAttachmentSize(attachments)).toBe(600)
  })
})

describe('max total attachment size', () => {
  it('is 10 MB', () => {
    expect(MAX_TOTAL_ATTACHMENT_SIZE).toBe(10 * 1024 * 1024)
  })
})

describe('attachment cache', () => {
  it('returns empty array for unknown agent', () => {
    expect(getAttachments('nonexistent')).toEqual([])
  })

  it('stores and retrieves attachments', () => {
    const attachments = [{ id: 'a1', filename: 'test.png' } as FileAttachment]
    setAttachments('agent-1', attachments)
    expect(getAttachments('agent-1')).toBe(attachments)
  })

  it('clears attachments', () => {
    setAttachments('agent-2', [{ id: 'a2' } as FileAttachment])
    clearAttachments('agent-2')
    expect(getAttachments('agent-2')).toEqual([])
  })
})

const PASTED_IMAGE_RE = /^Pasted Image \d+$/

describe('nextPastedImageName', () => {
  it('increments counter per agent', () => {
    const name1 = nextPastedImageName('paste-agent-1')
    const name2 = nextPastedImageName('paste-agent-1')
    expect(name1).toMatch(PASTED_IMAGE_RE)
    expect(name2).toMatch(PASTED_IMAGE_RE)
    // Second should have a higher number
    const num1 = Number.parseInt(name1.replace('Pasted Image ', ''), 10)
    const num2 = Number.parseInt(name2.replace('Pasted Image ', ''), 10)
    expect(num2).toBe(num1 + 1)
  })
})

describe('readFileAsAttachment', () => {
  it('reads a file and returns attachment with Uint8Array data', async () => {
    const content = new Uint8Array([137, 80, 78, 71]) // PNG header bytes
    const file = new File([content], 'test.png', { type: 'image/png' })

    const attachment = await readFileAsAttachment(file)

    expect(attachment.id).toBeTruthy()
    expect(attachment.filename).toBe('test.png')
    expect(attachment.mimeType).toBe('image/png')
    expect(attachment.size).toBe(4)
    expect(attachment.data).toBeInstanceOf(Uint8Array)
    expect(attachment.data).toEqual(content)
    expect(attachment.file).toBe(file)
  })

  it('uses custom filename when provided', async () => {
    const file = new File(['hello'], 'original.txt', { type: 'image/png' })
    const attachment = await readFileAsAttachment(file, 'custom.png')
    expect(attachment.filename).toBe('custom.png')
  })
})
