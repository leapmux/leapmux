import type { FileAttachment } from './attachments'
import type { AttachmentCapabilities } from './providers/registry'
import { describe, expect, it, vi } from 'vitest'
import { MAX_AGENT_INPUT_ITEM_BYTES } from '~/generated/contracts/agent-input'
import {
  buildAcceptAttribute,
  clearAttachments,
  collectDroppedAttachmentFiles,
  describeUnsupportedAttachment,
  forgetAgentAttachments,
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
import { queueEditDraftKey } from './composer/draftIdentity'

const claudeCapabilities: AttachmentCapabilities = {
  text: true,
  image: true,
  pdf: true,
  audio: false,
  binary: false,
}

const textAndImageCapabilities: AttachmentCapabilities = {
  text: true,
  image: true,
  pdf: false,
  audio: false,
  binary: false,
}

describe('inferAttachmentDetails', () => {
  it('does not decode known media or binary data as text', () => {
    const decode = vi.spyOn(TextDecoder.prototype, 'decode')
    try {
      const bytes = new TextEncoder().encode('Media bytes without a NUL byte.')
      for (const [filename, mime] of [
        ['voice.wav', 'audio/wav'],
        ['image.png', 'image/png'],
        ['document.pdf', 'application/pdf'],
        ['archive.zip', 'application/zip'],
      ] as const) {
        inferAttachmentDetails(filename, mime, bytes)
      }
      expect(decode).not.toHaveBeenCalled()
    }
    finally {
      decode.mockRestore()
    }
  })
  it.each([
    ['native.doc', 'application/msword'],
    ['native.DOCX', 'application/vnd.openxmlformats-officedocument.wordprocessingml.document'],
    ['native.xls', 'application/vnd.ms-excel'],
    ['native.XLSX', 'application/vnd.openxmlformats-officedocument.spreadsheetml.sheet'],
  ])('infers the Office type of %s without treating its bytes as text', (filename, expectedMime) => {
    for (const given of ['', 'application/octet-stream']) {
      for (const data of [
        new Uint8Array(),
        new TextEncoder().encode('Office bytes'),
        new Uint8Array([0xFF, 0]),
      ])
        expect(inferAttachmentDetails(filename, given, data)).toEqual({ kind: 'binary', mimeType: expectedMime })
    }
  })

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
  it('allows text across the declared attachment policies', () => {
    expect(isAttachmentSupported('text', claudeCapabilities)).toBe(true)
    expect(isAttachmentSupported('text', textAndImageCapabilities)).toBe(true)
  })

  it('rejects pdf and binary for a text and image policy', () => {
    expect(isAttachmentSupported('pdf', textAndImageCapabilities)).toBe(false)
    expect(isAttachmentSupported('binary', textAndImageCapabilities)).toBe(false)
  })

  it('builds provider-specific rejection copy', () => {
    expect(describeUnsupportedAttachment('pdf', 'Codex')).toBe('Codex does not support PDF attachments')
    expect(describeUnsupportedAttachment('binary', 'Claude Code')).toBe('Claude Code does not support binary attachments')
  })
})

describe('buildAcceptAttribute', () => {
  it('includes text extensions for non-binary providers', () => {
    const accept = buildAcceptAttribute(textAndImageCapabilities)!
    expect(accept).toContain('.css')
    expect(accept).toContain('.csv')
    expect(accept).toContain('image/png')
    expect(accept).not.toContain('application/pdf')
  })

  it('omits accept when provider supports arbitrary binary attachments', () => {
    expect(buildAcceptAttribute({
      text: true,
      image: true,
      pdf: true,
      audio: false,
      binary: true,
    })).toBeUndefined()
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
    // The limit comes from contracts/agent-input.json, so the Worker enforces
    // the same number. Restating the literal here would recreate the second
    // copy the contract exists to remove.
    expect(MAX_TOTAL_ATTACHMENT_SIZE).toBe(MAX_AGENT_INPUT_ITEM_BYTES)
  })
})

describe('attachment cache', () => {
  it('returns empty array for unknown agent', () => {
    expect(getAttachments('nonexistent')).toEqual([])
  })

  it('stores and retrieves attachments', () => {
    const attachments = [
      { id: 'a1', filename: 'test.png' } as FileAttachment,
    ]
    setAttachments('agent-1', attachments)
    expect(getAttachments('agent-1')).toBe(attachments)
  })

  it('clears attachments', () => {
    setAttachments('agent-2', [
      { id: 'a2' } as FileAttachment,
    ])
    clearAttachments('agent-2')
    expect(getAttachments('agent-2')).toEqual([])
  })

  it('forgets normal and queue-edit attachments for a closed agent', () => {
    setAttachments('agent-3', [
      { id: 'normal' } as FileAttachment,
    ])
    setAttachments('agent-3-queue-input-1', [
      { id: 'queued' } as FileAttachment,
    ])
    setAttachments('agent-30', [
      { id: 'other' } as FileAttachment,
    ])

    forgetAgentAttachments('agent-3')

    expect(getAttachments('agent-3')).toEqual([])
    expect(getAttachments('agent-3-queue-input-1')).toEqual([])
    expect(getAttachments('agent-30')).toHaveLength(1)
  })
})

describe('queueEditDraftKey', () => {
  it('produces a key that the agent sweep forgets', () => {
    const key = queueEditDraftKey('agent-sweep', 'input-1')
    setAttachments(key, [
      { id: 'queued' } as FileAttachment,
    ])

    forgetAgentAttachments('agent-sweep')

    expect(getAttachments(key)).toEqual([])
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

/** Create a minimal DataTransfer-like object for testing. */
function mockDataTransfer(items: { webkitGetAsEntry: () => unknown }[]): DataTransfer {
  return { items, files: [] } as unknown as DataTransfer
}

describe('collectDroppedAttachmentFiles', () => {
  function createFileEntry(path: string, file: File) {
    const parts = path.split('/')
    const name = parts.at(-1) ?? file.name
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
    const logo = createFileEntry('project/assets/logo.png', new File([
      new Uint8Array([1, 2, 3]),
    ], 'logo.png', { type: 'image/png' }))
    const assets = createDirectoryEntry('assets', [logo])
    const project = createDirectoryEntry('project', [assets, readme])
    const dataTransfer = mockDataTransfer([
      {
        webkitGetAsEntry: () => project,
      },
    ])

    const result = await collectDroppedAttachmentFiles(dataTransfer)

    expect(result.sizeLimitHit).toBe(false)
    expect(result.files.map(file => file.filename)).toEqual([
      'project/assets/logo.png',
      'project/README.md',
    ])
  })

  it('stops traversal immediately once the size limit would be exceeded', async () => {
    const first = createFileEntry('big/first.txt', new File([
      new Uint8Array(64),
    ], 'first.txt', { type: 'text/plain' }))
    const tooLarge = createFileEntry(
      'big/second.txt',
      new File([
        new Uint8Array(64),
      ], 'second.txt', { type: 'text/plain' }),
    )
    const late = createFileEntry('big/late.txt', new File([
      new Uint8Array(64),
    ], 'late.txt', { type: 'text/plain' }))
    const directory = createDirectoryEntry('big', [first, tooLarge, late])
    const dataTransfer = mockDataTransfer([
      {
        webkitGetAsEntry: () => directory,
      },
    ])

    const result = await collectDroppedAttachmentFiles(dataTransfer, MAX_TOTAL_ATTACHMENT_SIZE - 96)

    expect(result.sizeLimitHit).toBe(true)
    expect(result.files.map(file => file.filename)).toEqual(['big/first.txt'])
  })

  it('keeps readable files before and after an unreadable directory entry', async () => {
    const first = createFileEntry('project/a.txt', new File(['first'], 'a.txt'))
    const broken = {
      ...createFileEntry('project/b.txt', new File([], 'b.txt')),
      file: (_resolve: (file: File) => void, reject: (error: DOMException) => void) => reject(new DOMException('Unavailable', 'NotReadableError')),
    }
    const last = createFileEntry('project/c.txt', new File(['last'], 'c.txt'))
    const transfer = mockDataTransfer([
      {
        webkitGetAsEntry: () => createDirectoryEntry('project', [first, broken, last]),
      },
    ])
    const result = await collectDroppedAttachmentFiles(transfer)
    expect(result.files.map(file => file.filename)).toEqual(['project/a.txt', 'project/c.txt'])
    expect(result).toMatchObject({
      sizeLimitHit: false,
      failures: [
        { path: 'project/b.txt' },
      ],
    })
  })

  it('keeps entries from earlier batches when directory enumeration fails', async () => {
    const first = createFileEntry('project/a.txt', new File(['first'], 'a.txt'))
    let batches = 0
    const directory = {
      ...createDirectoryEntry('project', []),
      createReader: () => ({
        readEntries: (resolve: (entries: unknown[]) => void, reject: (error: DOMException) => void) => {
          if (batches++ === 0)
            resolve([first])
          else
            reject(new DOMException('Unavailable', 'NotReadableError'))
        },
      }),
    }
    const result = await collectDroppedAttachmentFiles(mockDataTransfer([
      {
        webkitGetAsEntry: () => directory,
      },
    ]))
    expect(result.files.map(file => file.filename)).toEqual(['project/a.txt'])
    expect(result).toMatchObject({
      sizeLimitHit: false,
      failures: [
        { path: 'project' },
      ],
    })
  })

  it('keeps other dropped entries when one entry lookup fails', async () => {
    const first = createFileEntry('first.txt', new File(['first'], 'first.txt'))
    const last = createFileEntry('last.txt', new File(['last'], 'last.txt'))
    const transfer = mockDataTransfer([
      {
        webkitGetAsEntry: () => first,
      },
      {
        webkitGetAsEntry: () => { throw new DOMException('Unavailable', 'NotReadableError') },
      },
      {
        webkitGetAsEntry: () => last,
      },
    ])
    const result = await collectDroppedAttachmentFiles(transfer)
    expect(result.files.map(file => file.filename)).toEqual(['first.txt', 'last.txt'])
    expect(result).toMatchObject({
      failures: [{}],
    })
  })
})

describe('native audio MIME inference', () => {
  it.each([
    ['tone.flac', 'audio/flac'],
    ['tone.AAC', 'audio/aac'],
  ])('infers %s as audio for empty, text-like and binary bytes', (filename, mimeType) => {
    for (const supplied of ['', 'application/octet-stream']) {
      for (const data of [
        new Uint8Array(),
        new TextEncoder().encode('audio bytes'),
        new Uint8Array([0xFF, 0]),
      ])
        expect(inferAttachmentDetails(filename, supplied, data)).toEqual({ kind: 'audio', mimeType })
    }
    expect(inferAttachmentDetails(filename, 'text/plain', new TextEncoder().encode('explicit text'))).toEqual({ kind: 'text', mimeType: 'text/plain' })
  })

  it.each([
    ['sample.wav', 'audio/wav'],
    ['sample.MP3', 'audio/mpeg'],
    ['sample.m4a', 'audio/mp4'],
    ['sample.webm', 'audio/webm'],
    ['sample.ogg', 'audio/ogg'],
  ])('keeps %s audio when its bytes are valid UTF-8 and MIME is absent', (filename, mimeType) => {
    expect(inferAttachmentDetails(filename, '', new Uint8Array([0, 1, 2]))).toEqual({ kind: 'audio', mimeType })
    expect(inferAttachmentDetails(filename, 'application/octet-stream', new Uint8Array([0, 1, 2]))).toEqual({ kind: 'audio', mimeType })
  })
})

describe('restricted audio attachments', () => {
  const capabilities: AttachmentCapabilities = {
    text: true,
    image: false,
    pdf: false,
    audio: ['audio/wav'],
    binary: false,
  }

  it('allows only the declared audio MIME types', () => {
    expect(isAttachmentSupported('audio', capabilities, 'audio/wav')).toBe(true)
    expect(isAttachmentSupported('audio', capabilities, ' AUDIO/WAV ')).toBe(true)
    expect(isAttachmentSupported('audio', capabilities, 'audio/flac')).toBe(false)
    expect(isAttachmentSupported('audio', capabilities)).toBe(false)
    expect(isAttachmentSupported('audio', { ...capabilities, audio: [] }, 'audio/wav')).toBe(false)
    expect(isAttachmentSupported('audio', { ...capabilities, audio: true }, 'audio/flac')).toBe(true)
    expect(isAttachmentSupported('audio', { ...capabilities, audio: false, binary: true }, 'audio/wav')).toBe(false)
    expect(isAttachmentSupported('audio', undefined, 'audio/wav')).toBe(false)
  })

  it('lists restricted MIME types without allowing arbitrary binaries', () => {
    const accepted = buildAcceptAttribute(capabilities)?.split(',')
    expect(accepted).toContain('audio/wav')
    expect(accepted).toContain('.txt')
    expect(accepted).not.toContain('.wav')
    expect(accepted).not.toContain('application/zip')
    expect(buildAcceptAttribute({ ...capabilities, binary: true })).toBeUndefined()
    expect(describeUnsupportedAttachment('audio', 'Codex', capabilities)).toBe('Codex does not support this audio file type')
  })

  it('keeps a restricted binary policy independent from audio', () => {
    const restricted = {
      ...capabilities,
      audio: false,
      binary: ['application/zip'],
    }
    expect(isAttachmentSupported('binary', restricted, 'application/zip')).toBe(true)
    expect(isAttachmentSupported('binary', restricted, 'application/octet-stream')).toBe(false)
    expect(isAttachmentSupported('audio', restricted, 'audio/wav')).toBe(false)
  })
})

describe('native file MIME inference', () => {
  it('keeps a recognized image binary type when MIME is absent', () => {
    expect(inferAttachmentDetails('image.png', '', new Uint8Array([137, 80, 78, 71]))).toEqual({ kind: 'image', mimeType: 'image/png' })
  })

  it('keeps a recognized PDF type when MIME is generic and bytes are valid UTF-8', () => {
    expect(inferAttachmentDetails('notes.pdf', 'application/octet-stream', new TextEncoder().encode('%PDF-1.4'))).toEqual({ kind: 'pdf', mimeType: 'application/pdf' })
  })
})

describe('independent audio attachment kind', () => {
  it('classifies audio independently from arbitrary binary data', () => {
    expect(inferAttachmentDetails('voice.wav', 'audio/wav', new Uint8Array([0, 1]))).toEqual({ kind: 'audio', mimeType: 'audio/wav' })
  })
})
