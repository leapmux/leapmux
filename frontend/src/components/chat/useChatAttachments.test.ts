import { createRoot, createSignal } from 'solid-js'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { showWarnToast } from '~/components/common/Toast'
import { AgentProvider } from '~/generated/proto/leapmux/v1/agent_pb'
import { forgetAgentAttachments, getAttachments, MAX_TOTAL_ATTACHMENT_SIZE } from './attachments'
import { queueEditDraftKey } from './composer/draftIdentity'
import { useChatAttachments } from './useChatAttachments'

vi.mock('./providers/registry', () => ({
  providerFor: () => ({
    configuration: {
      attachments: {
        text: true,
        image: true,
        pdf: false,
        audio: ['audio/wav'],
        binary: false,
      },
    },
  }),
}))
vi.mock('~/components/common/Toast', () => ({
  showWarnToast: vi.fn(),
}))

const disposals: (() => void)[] = []

afterEach(() => {
  for (const dispose of disposals.splice(0))
    dispose()
  for (const key of ['draft-a', 'draft-b'])
    forgetAgentAttachments(key)
  vi.restoreAllMocks()
  vi.mocked(showWarnToast).mockClear()
})

function attachmentDraft() {
  const [key, setKey] = createSignal('draft-a')
  let dispose = () => {}
  const state = createRoot((cleanup) => {
    dispose = cleanup
    return useChatAttachments({
      agentId: key,
      agentProvider: () => AgentProvider.CODEX,
      providerLabel: () => 'Codex',
    })
  })
  disposals.push(dispose)
  return { state, setKey, dispose }
}

function controlledFileReads() {
  const reads: FileReader[] = []
  const waiters: { count: number, resolve: () => void }[] = []
  vi.spyOn(FileReader.prototype, 'readAsArrayBuffer').mockImplementation(function (this: FileReader) {
    reads.push(this)
    for (const waiter of waiters) {
      if (reads.length >= waiter.count)
        waiter.resolve()
    }
  })
  return {
    count: () => reads.length,
    waitForCount: (count: number) => reads.length >= count
      ? Promise.resolve()
      : new Promise<void>(resolve => waiters.push({ count, resolve })),
    finish(index: number) {
      const reader = reads[index]
      expect(reader).toBeDefined()
      Object.defineProperty(reader!, 'result', { configurable: true, value: new Uint8Array([1]).buffer })
      reader!.onload?.(new ProgressEvent('load') as ProgressEvent<FileReader>)
    },
    fail(index: number) {
      const reader = reads[index]
      expect(reader).toBeDefined()
      Object.defineProperty(reader!, 'error', {
        configurable: true,
        value: new DOMException('The file is unavailable.', 'NotReadableError'),
      })
      reader!.onerror?.(new ProgressEvent('error') as ProgressEvent<FileReader>)
    },
  }
}

describe('useChatAttachments', () => {
  it('keeps a pending read when reactive owner metadata changes without changing the draft key', async () => {
    const [owner, setOwner] = createSignal({ id: 'draft-a', revision: 1 })
    const state = createRoot((dispose) => {
      disposals.push(dispose)
      return useChatAttachments({
        agentId: () => owner().id,
        agentProvider: () => AgentProvider.CODEX,
        providerLabel: () => 'Codex',
      })
    })
    const reads = controlledFileReads()
    const pending = state.addFiles([
      new File(['file'], 'file.txt'),
    ])
    setOwner({ id: 'draft-a', revision: 2 })
    reads.finish(0)
    expect(await pending).toBe(1)
    expect(state.attachments().map(file => file.filename)).toEqual(['file.txt'])
  })

  it.each([
    'draft-b',
    queueEditDraftKey('draft-a', 'queued-input'),
  ])('keeps a pending read out of the destination draft %s', async (destination) => {
    const { state, setKey } = attachmentDraft()
    const reads = controlledFileReads()
    const pending = state.addFiles([
      new File(['source'], 'source.txt'),
    ])
    setKey(destination)
    const destinationFiles = state.attachments()
    reads.finish(0)
    expect(await pending).toBe(0)
    expect(state.attachments()).toEqual(destinationFiles)
    expect(getAttachments(destination)).toEqual(destinationFiles)
  })

  it('invalidates a read when its draft is left and opened again', async () => {
    const { state, setKey } = attachmentDraft()
    const reads = controlledFileReads()
    const pending = state.addFiles([
      new File(['source'], 'source.txt'),
    ])
    setKey('draft-b')
    setKey('draft-a')
    reads.finish(0)
    expect(await pending).toBe(0)
    expect(state.attachments()).toEqual([])
  })

  it.each([
    [0, 1],
    [1, 0],
  ])('keeps concurrent additions completed in order %j', async (first, second) => {
    const { state } = attachmentDraft()
    const reads = controlledFileReads()
    const pending = [
      state.addFiles([
        new File(['a'], 'a.txt'),
      ]),
      state.addFiles([
        new File(['b'], 'b.txt'),
      ]),
    ]
    reads.finish(first)
    expect(await pending[first]).toBe(1)
    reads.finish(second)
    expect(await pending[second]).toBe(1)
    expect(state.attachments().map(file => file.filename).toSorted()).toEqual(['a.txt', 'b.txt'])
    expect(getAttachments('draft-a')).toEqual(state.attachments())
  })

  it('keeps an intervening removal when another file finishes reading', async () => {
    const { state } = attachmentDraft()
    await state.addFiles([
      new File(['old'], 'old.txt'),
    ])
    const removed = state.attachments()[0]!
    const reads = controlledFileReads()
    const pending = state.addFiles([
      new File(['new'], 'new.txt'),
    ])
    state.removeAttachment(removed.id)
    reads.finish(0)
    expect(await pending).toBe(1)
    expect(state.attachments().map(file => file.filename)).toEqual(['new.txt'])
  })

  it.each(['clear', 'replace'] as const)('invalidates reads when the draft receives a %s operation', async (operation) => {
    const { state } = attachmentDraft()
    const reads = controlledFileReads()
    const pending = state.addFiles([
      new File(['old'], 'old.txt'),
    ])
    if (operation === 'clear')
      state.clearAllAttachments()
    else
      state.replaceAttachments([])
    reads.finish(0)
    expect(await pending).toBe(0)
    expect(state.attachments()).toEqual([])
    expect(getAttachments('draft-a')).toEqual([])
  })

  it('does not recreate a forgotten cache after disposal', async () => {
    const { state, dispose } = attachmentDraft()
    const reads = controlledFileReads()
    const pending = state.addFiles([
      new File(['old'], 'old.txt'),
    ])
    dispose()
    forgetAgentAttachments('draft-a')
    reads.finish(0)
    expect(await pending).toBe(0)
    expect(getAttachments('draft-a')).toEqual([])
  })

  it('does not restore a forgotten draft cache when switching away', async () => {
    const { state, setKey } = attachmentDraft()
    await state.addFiles([
      new File(['old'], 'old.txt'),
    ])
    forgetAgentAttachments('draft-a')
    setKey('draft-b')
    expect(getAttachments('draft-a')).toEqual([])
  })

  it('checks the combined size again when concurrent additions commit', async () => {
    const { state } = attachmentDraft()
    const reads = controlledFileReads()
    const file = (name: string) => {
      const value = new File(['x'], name)
      Object.defineProperty(value, 'size', { value: MAX_TOTAL_ATTACHMENT_SIZE })
      return value
    }
    const first = state.addFiles([
      file('a.txt'),
    ])
    const second = state.addFiles([
      file('b.txt'),
    ])
    reads.finish(0)
    expect(await first).toBe(1)
    reads.finish(1)
    expect(await second).toBe(0)
    expect(state.attachments().map(file => file.filename)).toEqual(['a.txt'])
  })

  it('keeps directory traversal attached to its original draft lifetime', async () => {
    const { state, setKey } = attachmentDraft()
    let deliverFile: ((file: File) => void) | undefined
    const transfer = {
      items: [
        {
          webkitGetAsEntry: () => ({
            isFile: true,
            isDirectory: false,
            name: 'source.txt',
            fullPath: '/source.txt',
            file: (success: (file: File) => void) => { deliverFile = success },
          }),
        },
      ],
    } as unknown as DataTransfer
    const pending = state.addDroppedDataTransfer(transfer)
    setKey('draft-b')
    deliverFile!(new File(['source'], 'source.txt'))
    expect(await pending).toBe(0)
    expect(state.attachments()).toEqual([])
    expect(getAttachments('draft-b')).toEqual([])
  })

  it('keeps readable files from a selection that contains an unreadable file', async () => {
    const { state } = attachmentDraft()
    const reads = controlledFileReads()
    const pending = state.addFiles([
      new File(['a'], 'first.txt'),
      new File(['b'], 'broken.txt'),
      new File(['c'], 'last.txt'),
    ])
    const outcome = pending.then(count => ({ count }), error => ({ error }))
    reads.finish(0)
    await Promise.resolve()
    reads.fail(1)
    await Promise.resolve()
    expect(reads.count()).toBe(3)
    reads.finish(2)
    expect(await outcome).toEqual({ count: 2 })
    expect(state.attachments().map(file => file.filename)).toEqual(['first.txt', 'last.txt'])
    expect(showWarnToast).toHaveBeenCalledWith(expect.stringContaining('broken.txt'))
  })

  it('preserves existing files when every selected file is unreadable', async () => {
    const { state } = attachmentDraft()
    await state.addFiles([
      new File(['old'], 'old.txt'),
    ])
    const existing = state.attachments()
    const reads = controlledFileReads()
    const pending = state.addFiles([
      new File(['broken'], 'broken.txt'),
    ])
    reads.fail(0)
    expect(await pending).toBe(0)
    expect(state.attachments()).toEqual(existing)
    expect(getAttachments('draft-a')).toEqual(existing)
    expect(showWarnToast).toHaveBeenCalledWith(expect.stringContaining('broken.txt'))
  })

  it('reports a failed file selection and resets its input without rejecting', async () => {
    const { state } = attachmentDraft()
    const reads = controlledFileReads()
    const input = document.createElement('input')
    input.type = 'file'
    Object.defineProperty(input, 'files', {
      value: [
        new File(['broken'], 'broken.txt'),
      ],
    })
    Object.defineProperty(input, 'value', { writable: true, value: 'selected-file' })
    state.handleFileInputChange(input)
    expect(input.value).toBe('')
    reads.fail(0)
    await Promise.resolve()
    expect(state.attachments()).toEqual([])
    expect(showWarnToast).toHaveBeenCalledWith(expect.stringContaining('broken.txt'))
  })

  it('reports a directory-entry failure without rejecting the drop', async () => {
    const { state } = attachmentDraft()
    const transfer = {
      items: [
        {
          webkitGetAsEntry: () => ({
            isFile: true,
            isDirectory: false,
            name: 'broken.txt',
            fullPath: '/broken.txt',
            file: (_success: (file: File) => void, failure: (error: DOMException) => void) => {
              failure(new DOMException('The entry is unavailable.', 'NotReadableError'))
            },
          }),
        },
      ],
    } as unknown as DataTransfer
    expect(await state.addDroppedDataTransfer(transfer)).toBe(0)
    expect(state.attachments()).toEqual([])
    expect(showWarnToast).toHaveBeenCalledWith('Could not read the dropped files: broken.txt')
  })

  it('reports an unreadable dropped entry and adds the other files', async () => {
    const { state } = attachmentDraft()
    const reads = controlledFileReads()
    const transfer = {
      items: ['first.txt', 'broken.txt', 'last.txt'].map(name => ({
        webkitGetAsEntry: () => ({
          isFile: true,
          isDirectory: false,
          name,
          fullPath: `/${name}`,
          file: (success: (file: File) => void, failure: (error: DOMException) => void) => {
            if (name === 'broken.txt')
              failure(new DOMException('Unavailable', 'NotReadableError'))
            else
              success(new File(['readable'], name, { type: 'text/plain' }))
          },
        }),
      })),
    } as unknown as DataTransfer
    const pending = state.addDroppedDataTransfer(transfer)
    await reads.waitForCount(1)
    reads.finish(0)
    await reads.waitForCount(2)
    reads.finish(1)
    expect(await pending).toBe(2)
    expect(state.attachments().map(file => file.filename)).toEqual(['first.txt', 'last.txt'])
    expect(showWarnToast).toHaveBeenCalledWith('Could not read the dropped files: broken.txt')
  })

  it('accepts inferred audio and rejects unrelated binary files in the same selection', async () => {
    vi.mocked(showWarnToast).mockClear()
    let dispose = () => {}
    const state = createRoot((cleanup) => {
      dispose = cleanup
      return useChatAttachments({
        agentId: () => 'restricted-audio',
        agentProvider: () => AgentProvider.CODEX,
        providerLabel: () => 'Codex',
      })
    })
    try {
      const count = await state.addFiles([
        new File([
          new Uint8Array([0, 1, 2]),
        ], 'voice.wav'),
        new File([
          new Uint8Array([0, 255]),
        ], 'archive.bin', { type: 'application/octet-stream' }),
        new File([
          new Uint8Array([0, 255]),
        ], 'unsupported.flac', { type: 'audio/flac' }),
      ])
      expect(count).toBe(1)
      expect(state.attachments().map(file => file.filename)).toEqual(['voice.wav'])
      expect(state.attachments()[0]?.mimeType).toBe('audio/wav')
      expect(showWarnToast).toHaveBeenCalledWith('Codex does not support binary attachments')
      expect(showWarnToast).toHaveBeenCalledWith('Codex does not support this audio file type')
      expect(state.acceptAttribute()).toContain('audio/wav')
    }
    finally {
      dispose()
      forgetAgentAttachments('restricted-audio')
    }
  })
})
