import type { FileAttachment } from './attachments'
import type { AttachmentCapabilities } from './providers/registry'
import { describe, expect, it } from 'vitest'
import {
  buildAcceptAttribute,
  clearAttachments,
  collectDroppedAttachmentFiles,
  describeUnsupportedAttachment,
  getAttachments,
  inferAttachmentDetails,
  isAttachmentSupported,
  isImageMimeType,
  MAX_TOTAL_ATTACHMENT_SIZE,
  nextPastedImageName,
  readFileAsAttachment,
  setAttachments,
  totalAttachmentSize,
} from './attachments'

const claudeCapabilities: AttachmentCapabilities = {
  text: true,
  image: true,
  pdf: true,
  binary: false,
}

const codexCapabilities: AttachmentCapabilities = {
  text: true,
  image: true,
  pdf: false,
  binary: false,
}

describe('inferAttachmentDetails', () => {
  it('accepts image types', () => {
    expect(inferAttachmentDetails('test.png', 'image/png', new Uint8Array([137, 80, 78, 71]))).toEqual({
      kind: 'image',
      mimeType: 'image/png',
    })
  })

  it('accepts pdf', () => {
    expect(inferAttachmentDetails('test.pdf', 'application/pdf', new Uint8Array([37, 80, 68, 70]))).toEqual({
      kind: 'pdf',
      mimeType: 'application/pdf',
    })
  })

  it('infers text mime from extension when browser omits it', () => {
    expect(inferAttachmentDetails('styles.css', '', new TextEncoder().encode('body {}'))).toEqual({
      kind: 'text',
      mimeType: 'text/css',
    })
  })

  it('treats extensionless utf8 files as text/plain', () => {
    expect(inferAttachmentDetails('README', '', new TextEncoder().encode('hello'))).toEqual({
      kind: 'text',
      mimeType: 'text/plain',
    })
  })

  it('classifies unknown binary as binary', () => {
    expect(inferAttachmentDetails('archive.bin', '', new Uint8Array([0, 255, 1]))).toEqual({
      kind: 'binary',
      mimeType: 'application/octet-stream',
    })
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

describe('attachment support', () => {
  it('allows text across Claude and Codex', () => {
    expect(isAttachmentSupported('text', claudeCapabilities)).toBe(true)
    expect(isAttachmentSupported('text', codexCapabilities)).toBe(true)
  })

  it('rejects pdf and binary for Codex', () => {
    expect(isAttachmentSupported('pdf', codexCapabilities)).toBe(false)
    expect(isAttachmentSupported('binary', codexCapabilities)).toBe(false)
  })

  it('builds provider-specific rejection copy', () => {
    expect(describeUnsupportedAttachment('pdf', 'Codex')).toBe('Codex does not support PDF attachments')
    expect(describeUnsupportedAttachment('binary', 'Claude Code')).toBe('Claude Code does not support binary attachments')
  })
})

describe('buildAcceptAttribute', () => {
  it('includes text extensions for non-binary providers', () => {
    const accept = buildAcceptAttribute(codexCapabilities)!
    expect(accept).toContain('.css')
    expect(accept).toContain('.csv')
    expect(accept).toContain('image/png')
    expect(accept).not.toContain('application/pdf')
  })

  it('omits accept when provider supports arbitrary binary attachments', () => {
    expect(buildAcceptAttribute({ text: true, image: true, pdf: true, binary: true })).toBeUndefined()
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

  it('infers text mime for files with empty browser mime', async () => {
    const file = new File(['name,value\nfoo,1\n'], 'report.csv', { type: '' })
    const attachment = await readFileAsAttachment(file)
    expect(attachment.mimeType).toBe('text/csv')
  })
})

describe('collectDroppedAttachmentFiles', () => {
  function createFileEntry(path: string, file: File) {
    const parts = path.split('/')
    const name = parts[parts.length - 1] ?? file.name
    return {
      isFile: true,
      isDirectory: false,
      name,
      fullPath: `/${path}`,
      file: (resolve: (value: File) => void) => resolve(file),
    }
  }

  function createDirectoryEntry(name: string, entries: unknown[]) {
    return {
      isFile: false,
      isDirectory: true,
      name,
      fullPath: `/${name}`,
      createReader: () => {
        let done = false
        return {
          readEntries: (resolve: (value: unknown[]) => void) => {
            if (done) {
              resolve([])
              return
            }
            done = true
            resolve(entries)
          },
        }
      },
    }
  }

  it('recursively expands directory drops and preserves relative paths', async () => {
    const readme = createFileEntry('project/README.md', new File(['# test'], 'README.md', { type: 'text/markdown' }))
    const logo = createFileEntry('project/assets/logo.png', new File([new Uint8Array([1, 2, 3])], 'logo.png', { type: 'image/png' }))
    const assets = createDirectoryEntry('assets', [logo])
    const project = createDirectoryEntry('project', [assets, readme])
    const dataTransfer = {
      items: [{ webkitGetAsEntry: () => project }],
      files: [],
    } as unknown as DataTransfer

    const result = await collectDroppedAttachmentFiles(dataTransfer)

    expect(result.sizeLimitHit).toBe(false)
    expect(result.files.map(file => file.filename)).toEqual([
      'project/assets/logo.png',
      'project/README.md',
    ])
  })

  it('stops traversal immediately once the size limit would be exceeded', async () => {
    const first = createFileEntry('big/first.txt', new File([new Uint8Array(64)], 'first.txt', { type: 'text/plain' }))
    const tooLarge = createFileEntry(
      'big/second.txt',
      new File([new Uint8Array(64)], 'second.txt', { type: 'text/plain' }),
    )
    const late = createFileEntry('big/late.txt', new File([new Uint8Array(64)], 'late.txt', { type: 'text/plain' }))
    const directory = createDirectoryEntry('big', [first, tooLarge, late])
    const dataTransfer = {
      items: [{ webkitGetAsEntry: () => directory }],
      files: [],
    } as unknown as DataTransfer

    const result = await collectDroppedAttachmentFiles(dataTransfer, MAX_TOTAL_ATTACHMENT_SIZE - 96)

    expect(result.sizeLimitHit).toBe(true)
    expect(result.files.map(file => file.filename)).toEqual(['big/first.txt'])
  })
})
