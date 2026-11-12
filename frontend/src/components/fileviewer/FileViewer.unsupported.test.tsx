import { fireEvent, render, screen, waitFor } from '@solidjs/testing-library'
import { createSignal } from 'solid-js'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { GitFileStatusCode } from '~/generated/proto/leapmux/v1/common_pb'
import { GitFileRef } from '~/generated/proto/leapmux/v1/git_pb'
import { PREFIX_FILE_SCROLL, sessionStorageGet, sessionStorageSet } from '~/lib/browserStorage'
import { MAX_FILE_IMAGE_BYTES } from '~/lib/imageBlocks'
import { holdComputedDiffs, requestedDiffInputs, resetHeldComputedDiffs } from '~/test-support/heldComputedDiffs'
import { platformBridgeFileSaveStubs } from '~/test-support/saveActionsMocks'
import { readResp, statResp } from '~/test-support/workerRpcMocks'

const statFileImpl = vi.fn()
const readFileImpl = vi.fn()
const readGitFileImpl = vi.fn(async (..._args: unknown[]) => ({
  exists: false,
  content: new Uint8Array(),
  totalSize: 0n,
}))
const downloadImpl = vi.fn((..._args: unknown[]) => Promise.resolve())

vi.mock('~/api/workerRpc', () => ({
  workerFile: {
    statFile: (...args: unknown[]) => statFileImpl(...args),
    readFile: (...args: unknown[]) => readFileImpl(...args),
  },
  workerGit: {
    // `.then` hands out a promise that the spy never saw. A vi.fn spy attaches
    // handlers to the promise it returns, which marks a rejection as handled and
    // would hide one that the viewer leaves unhandled.
    readGitFile: (...args: unknown[]) => readGitFileImpl(...args).then(resp => resp),
  },
}))

vi.mock('~/lib/fileDownload', () => ({
  downloadFileFromWorker: (...args: unknown[]) => downloadImpl(...args),
  saveFileAs: vi.fn(),
  saveFileToDownloads: vi.fn(),
}))

// Light-weight mocks for the heavy child views so we can assert which
// branch fired without dragging in syntax highlighting / virtualization.
vi.mock('~/components/fileviewer/TextFileView', () => ({
  TextFileView: (props: { filePath: string, content: Uint8Array }) => (
    <div data-testid="text-view" data-text={new TextDecoder().decode(props.content)}>{props.filePath}</div>
  ),
}))
vi.mock('~/components/fileviewer/MarkdownFileView', () => ({
  MarkdownFileView: (props: { filePath: string }) => (
    <div data-testid="markdown-view">{props.filePath}</div>
  ),
}))
vi.mock('~/components/fileviewer/ImageFileView', () => ({
  ImageFileView: (props: { filePath: string, content: Uint8Array }) => (
    <div data-testid="image-view" data-first-byte={props.content[0]}>{props.filePath}</div>
  ),
}))
vi.mock('~/components/fileviewer/HexView', () => ({
  HexView: (props: { totalSize: number }) => (
    <div data-testid="hex-view">{`hex ${props.totalSize}`}</div>
  ),
}))

const showWarnToastMock = vi.fn()
vi.mock('~/components/common/Toast', () => ({
  showWarnToast: (...args: unknown[]) => showWarnToastMock(...args),
}))

// Stub the preferences context — the tests don't exercise the
// reveal-after-download flow, but FileViewer reads `revealAfterDownload`
// during render.
vi.mock('~/context/PreferencesContext', () => ({
  usePreferences: () => ({
    revealAfterDownload: () => false,
    setRevealAfterDownload: () => {},
  }),
}))

// Web-mode by default — these tests target the browser anchor-click
// download flow.
vi.mock('~/api/platformBridge', () => ({
  isTauriApp: () => false,
  platformBridge: {
    revealInFileManager: () => Promise.resolve(),
    ...platformBridgeFileSaveStubs(),
  },
}))

vi.mock('~/components/chat/diff/diffWorkerClient', async () => (await import('~/test-support/heldComputedDiffs')).heldDiffWorkerClient())

const { FileViewer } = await import('~/components/fileviewer/FileViewer')

const MAX = 256 * 1024

describe('FileViewer dispatch with unsupported view', () => {
  beforeEach(() => {
    statFileImpl.mockReset()
    readFileImpl.mockReset()
    downloadImpl.mockReset()
    downloadImpl.mockResolvedValue(undefined)
    showWarnToastMock.mockReset()
  })

  it('opens an image larger than the text preview limit', async () => {
    const bytes = new Uint8Array(1024 * 1024)
    readFileImpl.mockResolvedValue(readResp(bytes))
    render(() => <FileViewer workerId="w1" filePath="/repo/screenshot.png" />)
    await waitFor(() => expect(screen.getByTestId('image-view')).toBeInTheDocument())
    expect(readFileImpl.mock.calls[0]?.[1].limit).toBeGreaterThanOrEqual(BigInt(bytes.length))
    expect(screen.queryByText('This image is too large to preview.')).not.toBeInTheDocument()
  })

  it('does not replace the current image with an earlier file read', async () => {
    const [path, setPath] = createSignal('/first.png')
    let finish!: (value: ReturnType<typeof readResp>) => void
    readFileImpl.mockImplementationOnce(() => new Promise((resolve) => {
      finish = resolve
    }))
      .mockResolvedValue(readResp(new Uint8Array([2])))
    render(() => <FileViewer workerId="w1" filePath={path()} />)
    await waitFor(() => expect(readFileImpl).toHaveBeenCalledOnce())
    setPath('/second.png')
    await waitFor(() => expect(screen.getByTestId('image-view')).toHaveAttribute('data-first-byte', '2'))
    finish(readResp(new Uint8Array([1])))
    await Promise.resolve()
    await Promise.resolve()
    expect(screen.getByTestId('image-view')).toHaveAttribute('data-first-byte', '2')
  })

  it('renders TextFileView for a small text file', async () => {
    const bytes = new TextEncoder().encode('hello')
    statFileImpl.mockResolvedValue(statResp(bytes.length))
    readFileImpl.mockResolvedValue(readResp(bytes))

    render(() => <FileViewer workerId="w1" filePath="/repo/notes.txt" />)
    await waitFor(() => expect(screen.getByTestId('text-view')).toBeInTheDocument())
    expect(screen.queryByText(/cannot be displayed inline/i)).not.toBeInTheDocument()
  })

  it('renders TextFileView (truncated) for a >256 KiB text file with the existing status bar warning', async () => {
    const partial = new TextEncoder().encode('a'.repeat(1024))
    const fullSize = MAX_FILE_IMAGE_BYTES + 1
    statFileImpl.mockResolvedValue(statResp(fullSize))
    readFileImpl.mockResolvedValue(readResp(partial, fullSize))

    render(() => <FileViewer workerId="w1" filePath="/repo/big.log" />)
    await waitFor(() => expect(screen.getByTestId('text-view')).toBeInTheDocument())
    // No unsupported view (text files render inline regardless of size).
    expect(screen.queryByText(/cannot be displayed inline/i)).not.toBeInTheDocument()
    expect(screen.queryByText(/too large to preview/i)).not.toBeInTheDocument()
    // Existing truncation warning still appears.
    expect(screen.getByText(/Truncated at 256(\.0)? KB/i)).toBeInTheDocument()
  })

  it('renders the unsupported view for a small .zip file WITHOUT calling readFile', async () => {
    statFileImpl.mockResolvedValue(statResp(2048))

    render(() => <FileViewer workerId="w1" filePath="/repo/archive.zip" />)
    await waitFor(() =>
      expect(screen.getByText('This file cannot be displayed inline.')).toBeInTheDocument(),
    )
    expect(readFileImpl).not.toHaveBeenCalled()
    expect(screen.queryByTestId('text-view')).not.toBeInTheDocument()
    expect(screen.queryByTestId('hex-view')).not.toBeInTheDocument()
  })

  it('renders the unsupported view for an image above the shared limit via metaOnlyIfTruncated (no wasted bytes)', async () => {
    // The image path now collapses statFile + readFile into a single
    // readFile call with metaOnlyIfTruncated=true. For an oversize
    // image, the server skips the bytes and returns just totalSize —
    // we never download the 256 KiB we'd refuse to render anyway.
    readFileImpl.mockResolvedValue(readResp(new Uint8Array(0), MAX_FILE_IMAGE_BYTES + 1))

    render(() => <FileViewer workerId="w1" filePath="/repo/huge.png" />)
    await waitFor(() =>
      expect(screen.getByText('This image is too large to preview.')).toBeInTheDocument(),
    )
    expect(readFileImpl).toHaveBeenCalledTimes(1)
    expect(readFileImpl).toHaveBeenCalledWith('w1', expect.objectContaining({
      path: '/repo/huge.png',
      metaOnlyIfTruncated: true,
    }), {
      signal: expect.any(AbortSignal),
    })
    expect(statFileImpl).not.toHaveBeenCalled()
  })

  it('renders ImageFileView for a small image in a single readFile (no statFile)', async () => {
    // Counterpart to the oversize case: a small image arrives as
    // content + totalSize in one round trip, no statFile needed.
    const bytes = new Uint8Array([0x89, 0x50, 0x4E, 0x47])
    readFileImpl.mockResolvedValue(readResp(bytes))

    render(() => <FileViewer workerId="w1" filePath="/repo/tiny.png" />)
    await waitFor(() => expect(screen.getByTestId('image-view')).toBeInTheDocument())
    expect(readFileImpl).toHaveBeenCalledTimes(1)
    expect(statFileImpl).not.toHaveBeenCalled()
  })

  it('renders the unsupported view for an unknown-extension binary (readFile called once for probe)', async () => {
    const bytes = new Uint8Array([0, 1, 2, 3, 0, 0, 0])
    statFileImpl.mockResolvedValue(statResp(bytes.length))
    readFileImpl.mockResolvedValue(readResp(bytes))

    render(() => <FileViewer workerId="w1" filePath="/repo/blob.foo" />)
    await waitFor(() =>
      expect(screen.getByText('This file cannot be displayed inline.')).toBeInTheDocument(),
    )
    expect(readFileImpl).toHaveBeenCalledTimes(1)
  })

  it('clicking Show anyway on a .zip lazy-fetches and renders HexView', async () => {
    statFileImpl.mockResolvedValue(statResp(2048))
    const bytes = new Uint8Array([0, 1, 2, 3])
    readFileImpl.mockResolvedValue(readResp(bytes, 2048))

    render(() => <FileViewer workerId="w1" filePath="/repo/archive.zip" />)
    await waitFor(() =>
      expect(screen.getByTestId('unsupported-show-anyway-button')).toBeInTheDocument(),
    )
    expect(readFileImpl).not.toHaveBeenCalled()

    fireEvent.click(screen.getByTestId('unsupported-show-anyway-button'))
    await waitFor(() => expect(screen.getByTestId('hex-view')).toBeInTheDocument())
    expect(readFileImpl).toHaveBeenCalledTimes(1)
    // The worker id and the request. The third argument holds the call
    // options, which this read leaves unset.
    expect(readFileImpl.mock.calls[0]?.[0]).toBe('w1')
    expect(readFileImpl.mock.calls[0]?.[1]).toMatchObject({
      path: '/repo/archive.zip',
      limit: BigInt(MAX),
    })
  })

  it('clicking Show anyway on an oversize image lazy-fetches and renders HexView', async () => {
    // First call: initial load with metaOnlyIfTruncated → empty content
    // + oversize totalSize. Second call: Show anyway → partial bytes
    // (the unsupported card lifts metaOnlyIfTruncated to actually fetch).
    const partial = new Uint8Array(1024)
    readFileImpl.mockResolvedValueOnce(readResp(new Uint8Array(0), MAX_FILE_IMAGE_BYTES + 1))
    readFileImpl.mockResolvedValueOnce(readResp(partial, MAX_FILE_IMAGE_BYTES + 1))

    render(() => <FileViewer workerId="w1" filePath="/repo/huge.png" />)
    await waitFor(() =>
      expect(screen.getByText('This image is too large to preview.')).toBeInTheDocument(),
    )
    expect(readFileImpl).toHaveBeenCalledTimes(1)

    fireEvent.click(screen.getByTestId('unsupported-show-anyway-button'))
    await waitFor(() => expect(screen.getByTestId('hex-view')).toBeInTheDocument())
    expect(readFileImpl).toHaveBeenCalledTimes(2)
  })

  it('clicking Show anyway on a probed-binary file renders HexView without re-fetching', async () => {
    const bytes = new Uint8Array([0, 1, 2, 3, 0, 0, 0])
    statFileImpl.mockResolvedValue(statResp(bytes.length))
    readFileImpl.mockResolvedValue(readResp(bytes))

    render(() => <FileViewer workerId="w1" filePath="/repo/blob.foo" />)
    await waitFor(() =>
      expect(screen.getByText('This file cannot be displayed inline.')).toBeInTheDocument(),
    )
    expect(readFileImpl).toHaveBeenCalledTimes(1)

    fireEvent.click(screen.getByTestId('unsupported-show-anyway-button'))
    await waitFor(() => expect(screen.getByTestId('hex-view')).toBeInTheDocument())
    // Still only one readFile — bytes were cached during the probe.
    expect(readFileImpl).toHaveBeenCalledTimes(1)
  })

  it('shows "Show download view" in the status bar when in Show-anyway mode', async () => {
    statFileImpl.mockResolvedValue(statResp(2048))
    readFileImpl.mockResolvedValue(readResp(new Uint8Array(4), 2048))

    render(() => <FileViewer workerId="w1" filePath="/repo/archive.zip" />)
    await waitFor(() =>
      expect(screen.getByTestId('unsupported-show-anyway-button')).toBeInTheDocument(),
    )
    fireEvent.click(screen.getByTestId('unsupported-show-anyway-button'))
    await waitFor(() => expect(screen.getByTestId('hex-view')).toBeInTheDocument())
    expect(screen.getByTestId('file-show-download-view-button')).toBeInTheDocument()
  })

  it('clicking "Show download view" returns to the unsupported view without re-fetching', async () => {
    statFileImpl.mockResolvedValue(statResp(2048))
    readFileImpl.mockResolvedValue(readResp(new Uint8Array(4), 2048))

    render(() => <FileViewer workerId="w1" filePath="/repo/archive.zip" />)
    await waitFor(() =>
      expect(screen.getByTestId('unsupported-show-anyway-button')).toBeInTheDocument(),
    )
    fireEvent.click(screen.getByTestId('unsupported-show-anyway-button'))
    await waitFor(() => expect(screen.getByTestId('hex-view')).toBeInTheDocument())
    expect(readFileImpl).toHaveBeenCalledTimes(1)

    fireEvent.click(screen.getByTestId('file-show-download-view-button'))
    await waitFor(() =>
      expect(screen.getByText('This file cannot be displayed inline.')).toBeInTheDocument(),
    )
    expect(screen.queryByTestId('hex-view')).not.toBeInTheDocument()
    expect(readFileImpl).toHaveBeenCalledTimes(1) // no re-fetch
  })

  it('clicking Download calls downloadFileFromWorker with (workerId, filePath)', async () => {
    statFileImpl.mockResolvedValue(statResp(2048))

    render(() => <FileViewer workerId="w-42" filePath="/repo/archive.zip" />)
    await waitFor(() =>
      expect(screen.getByTestId('unsupported-download-button')).toBeInTheDocument(),
    )
    fireEvent.click(screen.getByTestId('unsupported-download-button'))
    await waitFor(() => expect(downloadImpl).toHaveBeenCalledTimes(1))
    expect(downloadImpl).toHaveBeenCalledWith('w-42', '/repo/archive.zip', 'posix', expect.any(Function))
  })

  it('discards lazy-fetched bytes if filePath changes during the in-flight readFile', async () => {
    // First file: a .zip (no upfront fetch). User clicks "Show anyway".
    // While that readFile is in flight, the parent swaps filePath to a
    // text file. The readFile then resolves — the zip's bytes must NOT
    // be written into the new file's signal, and HexView must NOT show.
    statFileImpl.mockImplementation(async (_workerId, req) => {
      if (req.path === '/repo/archive.zip')
        return statResp(2048)
      // /repo/notes.txt — a small text file
      return statResp(11)
    })

    let resolveZipRead!: (resp: ReturnType<typeof readResp>) => void
    const pendingZipRead = new Promise<ReturnType<typeof readResp>>((res) => {
      resolveZipRead = res
    })
    const textBytes = new TextEncoder().encode('hello world')
    readFileImpl.mockImplementation(async (_workerId, req) => {
      if (req.path === '/repo/archive.zip')
        return await pendingZipRead
      // /repo/notes.txt
      return readResp(textBytes)
    })

    const [path, setPath] = createSignal('/repo/archive.zip')
    render(() => <FileViewer workerId="w1" filePath={path()} />)

    await waitFor(() =>
      expect(screen.getByTestId('unsupported-show-anyway-button')).toBeInTheDocument(),
    )
    fireEvent.click(screen.getByTestId('unsupported-show-anyway-button'))

    // Swap props mid-fetch.
    setPath('/repo/notes.txt')
    await waitFor(() => expect(screen.getByTestId('text-view')).toBeInTheDocument())

    // Now resolve the zip's readFile. The bail check must prevent the
    // bytes from clobbering the text-file state.
    resolveZipRead(readResp(new Uint8Array([0, 1, 2, 3]), 2048))
    await new Promise(r => setTimeout(r, 0))

    expect(screen.getByTestId('text-view')).toBeInTheDocument()
    expect(screen.queryByTestId('hex-view')).not.toBeInTheDocument()
  })

  it('does not issue any worker RPC when workerId or filePath is empty (tab rehydration race)', async () => {
    statFileImpl.mockResolvedValue(statResp(0))

    const [path, setPath] = createSignal('')
    render(() => <FileViewer workerId="w1" filePath={path()} />)

    // Initial mount with empty filePath: load effect must short-circuit
    // before issuing any worker RPC. Otherwise the worker rejects an
    // empty path with "access denied" and the error sticks even after
    // the tab restore fills the path in.
    await new Promise(r => setTimeout(r, 0))
    expect(statFileImpl).not.toHaveBeenCalled()
    expect(readFileImpl).not.toHaveBeenCalled()

    // Now the workspace-restore RPC arrives with the real path. A text
    // file goes straight to readFile (no preceding statFile — that's
    // reserved for the known-binary deny-list path; images use
    // readFile + metaOnlyIfTruncated and text/source/markdown skip it
    // entirely).
    const bytes = new TextEncoder().encode('hello')
    readFileImpl.mockResolvedValue(readResp(bytes))
    setPath('/repo/notes.txt')

    await waitFor(() => expect(screen.getByTestId('text-view')).toBeInTheDocument())
    expect(statFileImpl).not.toHaveBeenCalled()
    expect(readFileImpl).toHaveBeenCalledTimes(1)
    expect(showWarnToastMock).not.toHaveBeenCalled()
    expect(screen.queryByText(/access denied/i)).not.toBeInTheDocument()
  })

  it('surfaces failed downloads via a toast (no alert dialog)', async () => {
    statFileImpl.mockResolvedValue(statResp(2048))
    downloadImpl.mockRejectedValueOnce(new Error('disk full'))

    render(() => <FileViewer workerId="w1" filePath="/repo/archive.zip" />)
    await waitFor(() =>
      expect(screen.getByTestId('unsupported-download-button')).toBeInTheDocument(),
    )
    fireEvent.click(screen.getByTestId('unsupported-download-button'))
    await waitFor(() => expect(showWarnToastMock).toHaveBeenCalledTimes(1))
    expect(showWarnToastMock).toHaveBeenCalledWith(
      expect.stringContaining('Failed to download archive.zip'),
      expect.any(Error),
    )
  })
})

// A superseded or unmounted load must stop its RPCs, not only ignore their
// answers: a large read keeps the worker and the channel busy for a result
// that nobody waits for.
describe('FileViewer load cancellation', () => {
  beforeEach(() => {
    statFileImpl.mockReset()
    readFileImpl.mockReset()
    readGitFileImpl.mockClear()
  })

  /** The options that one RPC call received, which carry its abort signal. */
  function signalOf(call: unknown[] | undefined): AbortSignal | undefined {
    return (call?.[2] as { signal?: AbortSignal } | undefined)?.signal
  }

  /** A response that never arrives unless the test rejects it. */
  function pendingRpc() {
    let reject!: (err: unknown) => void
    const promise = new Promise<never>((_resolve, rejectPromise) => {
      reject = rejectPromise
    })
    return { promise, reject }
  }

  it('aborts the read of a superseded text load', async () => {
    const first = pendingRpc()
    readFileImpl.mockImplementationOnce(() => first.promise)
      .mockResolvedValue(readResp(new TextEncoder().encode('second')))
    const [path, setPath] = createSignal('/repo/first.txt')
    render(() => <FileViewer workerId="w1" filePath={path()} />)
    await waitFor(() => expect(readFileImpl).toHaveBeenCalledOnce())
    const signal = signalOf(readFileImpl.mock.calls[0])
    expect(signal?.aborted).toBe(false)

    setPath('/repo/second.txt')

    expect(signal?.aborted).toBe(true)
    await waitFor(() => expect(screen.getByTestId('text-view')).toHaveTextContent('/repo/second.txt'))
    expect(signalOf(readFileImpl.mock.calls[1])?.aborted).toBe(false)
  })

  it('aborts the stat of a superseded binary load', async () => {
    const first = pendingRpc()
    statFileImpl.mockImplementationOnce(() => first.promise)
      .mockResolvedValue(statResp(2048))
    const [path, setPath] = createSignal('/repo/first.zip')
    render(() => <FileViewer workerId="w1" filePath={path()} />)
    await waitFor(() => expect(statFileImpl).toHaveBeenCalledOnce())
    const signal = signalOf(statFileImpl.mock.calls[0])
    expect(signal?.aborted).toBe(false)

    setPath('/repo/second.zip')

    expect(signal?.aborted).toBe(true)
  })

  it('aborts the load when the viewer unmounts', async () => {
    const first = pendingRpc()
    readFileImpl.mockImplementationOnce(() => first.promise)
    const { unmount } = render(() => <FileViewer workerId="w1" filePath="/repo/notes.txt" />)
    await waitFor(() => expect(readFileImpl).toHaveBeenCalledOnce())

    unmount()

    expect(signalOf(readFileImpl.mock.calls[0])?.aborted).toBe(true)
  })

  // The abort rejects the superseded RPC. That rejection is the expected end
  // of a load nobody waits for, so it must not reach the screen as a failure
  // of the file that replaced it.
  it('ignores the abort of a superseded load', async () => {
    const first = pendingRpc()
    readFileImpl.mockImplementationOnce(() => first.promise)
      .mockResolvedValue(readResp(new TextEncoder().encode('second')))
    const [path, setPath] = createSignal('/repo/first.txt')
    render(() => <FileViewer workerId="w1" filePath={path()} />)
    await waitFor(() => expect(readFileImpl).toHaveBeenCalledOnce())

    setPath('/repo/second.txt')
    await waitFor(() => expect(screen.getByTestId('text-view')).toHaveTextContent('/repo/second.txt'))
    first.reject(new DOMException('The operation was aborted.', 'AbortError'))
    await new Promise(resolve => setTimeout(resolve, 0))

    expect(screen.getByTestId('text-view')).toHaveTextContent('/repo/second.txt')
    expect(screen.queryByText('The operation was aborted.')).not.toBeInTheDocument()
  })
})

// The ref and diff reads follow the same rule as the working-copy load: a
// newer mode or path ends the older read, and its late answer must not
// replace the view the user switched to.
describe('FileViewer ref and diff reads', () => {
  beforeEach(() => {
    statFileImpl.mockReset()
    readFileImpl.mockReset()
    readGitFileImpl.mockReset()
    readFileImpl.mockResolvedValue(readResp(new TextEncoder().encode('working')))
  })

  type GitRead = Awaited<ReturnType<typeof readGitFileImpl>>

  /** A git read that the test answers by hand. */
  function deferredGitRead() {
    let resolve!: (value: GitRead) => void
    const promise = new Promise<GitRead>((resolvePromise) => {
      resolve = resolvePromise
    })
    return { promise, resolve }
  }

  function gitText(text: string): GitRead {
    const content = new TextEncoder().encode(text)
    return {
      exists: true,
      content,
      totalSize: BigInt(content.length),
    }
  }

  it('keeps the staged copy when the earlier HEAD read answers last', async () => {
    const head = deferredGitRead()
    readGitFileImpl.mockImplementationOnce(() => head.promise)
      .mockResolvedValue(gitText('staged text'))
    const [mode, setMode] = createSignal<'head' | 'staged'>('head')
    render(() => <FileViewer workerId="w1" filePath="/repo/notes.txt" fileViewMode={mode()} />)
    await waitFor(() => expect(readGitFileImpl).toHaveBeenCalledOnce())

    setMode('staged')
    await waitFor(() => expect(screen.getByTestId('text-view')).toHaveAttribute('data-text', 'staged text'))
    head.resolve(gitText('head text'))
    await new Promise(resolve => setTimeout(resolve, 0))

    expect(screen.getByTestId('text-view')).toHaveAttribute('data-text', 'staged text')
  })

  it('aborts the git read of a superseded mode', async () => {
    const head = deferredGitRead()
    readGitFileImpl.mockImplementationOnce(() => head.promise)
      .mockResolvedValue(gitText('staged text'))
    const [mode, setMode] = createSignal<'head' | 'staged'>('head')
    render(() => <FileViewer workerId="w1" filePath="/repo/notes.txt" fileViewMode={mode()} />)
    await waitFor(() => expect(readGitFileImpl).toHaveBeenCalledOnce())
    const signal = (readGitFileImpl.mock.calls[0]?.[2] as { signal?: AbortSignal } | undefined)?.signal
    expect(signal?.aborted).toBe(false)

    setMode('staged')

    expect(signal?.aborted).toBe(true)
  })

  // Both sides of a staged diff start together. A mode switch aborts both,
  // and the viewer leaves at the staged side's rejection before it awaits the
  // HEAD side. The HEAD side's rejection must not go unhandled.
  it('handles both sides of a diff whose reads the viewer abandons', async () => {
    const unhandled: unknown[] = []
    const onUnhandled = (reason: unknown) => unhandled.push(reason)
    process.on('unhandledRejection', onUnhandled)
    try {
      // Each read rejects only when its caller aborts it.
      readGitFileImpl.mockImplementation((...args: unknown[]) => new Promise<GitRead>((_resolve, reject) => {
        const opts = args[2] as { signal?: AbortSignal } | undefined
        opts?.signal?.addEventListener('abort', () => reject(new DOMException('The operation was aborted.', 'AbortError')), { once: true })
      }))
      const [mode, setMode] = createSignal<'unified-diff' | 'working'>('unified-diff')
      render(() => (
        <FileViewer
          workerId="w1"
          filePath="/repo/notes.txt"
          fileViewMode={mode()}
          fileDiffBase="head-vs-staged"
          gitFileStatus={{ path: 'notes.txt' } as never}
        />
      ))
      await waitFor(() => expect(readGitFileImpl).toHaveBeenCalledTimes(2))

      setMode('working')
      await new Promise(resolve => setTimeout(resolve, 0))

      expect(unhandled).toEqual([])
      expect(screen.queryByText('Loading...'), 'the mode switch ended the reads that the flag waited for').not.toBeInTheDocument()
    }
    finally {
      process.off('unhandledRejection', onUnhandled)
    }
  })

  // Working mode reads nothing, so no read of that run can clear the flag. A
  // read that the switch abandoned cannot clear it either, because its run is
  // over. The working-mode run must clear the flag itself.
  describe.each([
    ['the HEAD copy', 'head', {}],
    ['the staged copy', 'staged', {}],
    [
      'a HEAD-vs-staged diff',
      'unified-diff',
      { fileDiffBase: 'head-vs-staged' as const },
    ],
    [
      'a HEAD-vs-working diff',
      'split-diff',
      { fileDiffBase: 'head-vs-working' as const },
    ],
  ] as const)('a return to working mode from %s', (_label, startMode, extraProps) => {
    /** Mount in `startMode` with the git reads held, then return to working mode. */
    async function switchToWorkingBeforeTheReadAnswers() {
      readGitFileImpl.mockImplementation(() => new Promise<GitRead>(() => {}))
      const [mode, setMode] = createSignal<'head' | 'staged' | 'unified-diff' | 'split-diff' | 'working'>(startMode)
      render(() => (
        <FileViewer
          workerId="w1"
          filePath="/repo/notes.txt"
          fileViewMode={mode()}
          gitFileStatus={{ path: 'notes.txt' } as never}
          {...extraProps}
        />
      ))
      await waitFor(() => expect(readGitFileImpl).toHaveBeenCalled())
      await waitFor(() => expect(readFileImpl).toHaveBeenCalled())
      expect(screen.getByText('Loading...'), 'the git read holds the indicator').toBeInTheDocument()

      setMode('working')
      await waitFor(() => expect(screen.getByTestId('text-view')).toBeInTheDocument())
    }

    it('clears the loading indicator', async () => {
      await switchToWorkingBeforeTheReadAnswers()

      expect(screen.queryByText('Loading...')).not.toBeInTheDocument()
    })

    // The restore effect returns while a read is pending, so a stuck flag
    // stops every later restore for this viewer.
    it('restores the saved scroll position of working mode', async () => {
      const key = `${PREFIX_FILE_SCROLL}w1:/repo/notes.txt:working`
      sessionStorageSet(key, 120)

      await switchToWorkingBeforeTheReadAnswers()

      expect(sessionStorageGet<number>(key), 'the restore consumes the saved position').toBeUndefined()
    })
  })

  it('keeps the indicator while a newer read is pending', async () => {
    const head = deferredGitRead()
    const staged = deferredGitRead()
    readGitFileImpl.mockImplementationOnce(() => head.promise)
      .mockImplementationOnce(() => staged.promise)
    const [mode, setMode] = createSignal<'head' | 'staged'>('head')
    render(() => <FileViewer workerId="w1" filePath="/repo/notes.txt" fileViewMode={mode()} />)
    await waitFor(() => expect(readGitFileImpl).toHaveBeenCalledOnce())

    setMode('staged')
    await waitFor(() => expect(readGitFileImpl).toHaveBeenCalledTimes(2))
    head.resolve(gitText('head text'))
    await new Promise(resolve => setTimeout(resolve, 0))
    expect(screen.getByText('Loading...'), 'the late answer of the older run must not clear the flag').toBeInTheDocument()

    staged.resolve(gitText('staged text'))
    await waitFor(() => expect(screen.getByTestId('text-view')).toHaveAttribute('data-text', 'staged text'))
    expect(screen.queryByText('Loading...')).not.toBeInTheDocument()
  })
})

// The HEAD and staged versions stop where the working copy does, at 256 KiB, and
// the status bar says so for the version that the current mode shows.
describe('FileViewer truncated git versions', () => {
  const TRUNCATED = /Truncated at 256(\.0)? KB/i
  const LIMIT = 256 * 1024

  beforeEach(() => {
    statFileImpl.mockReset()
    readFileImpl.mockReset()
    readGitFileImpl.mockReset()
    readFileImpl.mockResolvedValue(readResp(new TextEncoder().encode('working')))
  })

  function gitRead(text: string, totalSize: number) {
    return {
      exists: true,
      content: new TextEncoder().encode(text),
      totalSize: BigInt(totalSize),
    }
  }

  it('reads a git version up to the limit of the working copy', async () => {
    readGitFileImpl.mockResolvedValue(gitRead('head text', 9))
    render(() => <FileViewer workerId="w1" filePath="/repo/notes.txt" fileViewMode="head" />)
    await waitFor(() => expect(screen.getByTestId('text-view')).toHaveAttribute('data-text', 'head text'))

    expect(readGitFileImpl).toHaveBeenCalledWith('w1', expect.objectContaining({
      path: '/repo/notes.txt',
      limit: BigInt(LIMIT),
    }), expect.anything())
  })

  it('warns that the HEAD version shown is truncated', async () => {
    readGitFileImpl.mockResolvedValue(gitRead('head text', LIMIT + 1))
    render(() => <FileViewer workerId="w1" filePath="/repo/notes.txt" fileViewMode="head" />)
    await waitFor(() => expect(screen.getByTestId('text-view')).toHaveAttribute('data-text', 'head text'))

    expect(screen.getByText(TRUNCATED)).toBeInTheDocument()
  })

  it('does not warn for a whole HEAD version, even when the working copy is truncated', async () => {
    readFileImpl.mockResolvedValue(readResp(new TextEncoder().encode('working'), LIMIT + 1))
    readGitFileImpl.mockResolvedValue(gitRead('head text', 9))
    render(() => <FileViewer workerId="w1" filePath="/repo/notes.txt" fileViewMode="head" />)
    await waitFor(() => expect(screen.getByTestId('text-view')).toHaveAttribute('data-text', 'head text'))

    expect(screen.queryByText(TRUNCATED)).not.toBeInTheDocument()
  })

  it.each([
    [
      'the HEAD side',
      'head-vs-staged',
      [LIMIT + 1, 6],
      7,
    ],
    [
      'the staged side',
      'head-vs-staged',
      [4, LIMIT + 1],
      7,
    ],
    [
      'the working side',
      'head-vs-working',
      [4, 4],
      LIMIT + 1,
    ],
  ] as const)('warns that a diff is truncated when %s is cut', async (_side, base, [headSize, stagedSize], workingSize) => {
    readFileImpl.mockResolvedValue(readResp(new TextEncoder().encode('working'), workingSize))
    readGitFileImpl.mockImplementation(async (...args: unknown[]) => {
      const request = args[1] as { ref: number }
      return request.ref === GitFileRef.HEAD
        ? gitRead('head', headSize)
        : gitRead('staged', stagedSize)
    })
    render(() => (
      <FileViewer
        workerId="w1"
        filePath="/repo/notes.txt"
        fileViewMode="unified-diff"
        fileDiffBase={base}
        gitFileStatus={{ path: 'notes.txt' } as never}
      />
    ))
    await waitFor(() => expect(screen.getByText(TRUNCATED)).toBeInTheDocument())
  })

  it('does not warn for a diff of whole versions', async () => {
    readGitFileImpl.mockImplementation(async (...args: unknown[]) => {
      const request = args[1] as { ref: number }
      return request.ref === GitFileRef.HEAD
        ? gitRead('head', 4)
        : gitRead('staged', 6)
    })
    render(() => (
      <FileViewer
        workerId="w1"
        filePath="/repo/notes.txt"
        fileViewMode="unified-diff"
        fileDiffBase="head-vs-staged"
        gitFileStatus={{ path: 'notes.txt' } as never}
      />
    ))
    await waitFor(() => expect(readGitFileImpl).toHaveBeenCalledTimes(2))
    await waitFor(() => expect(screen.queryByText('Loading...')).not.toBeInTheDocument())

    expect(screen.queryByText(TRUNCATED)).not.toBeInTheDocument()
  })
})

// The diff worker computes the diff of the two versions. The viewer draws its
// loading state until the diff arrives.
describe('FileViewer diff mode', () => {
  beforeEach(() => {
    statFileImpl.mockReset()
    readFileImpl.mockReset()
    readGitFileImpl.mockReset()
  })

  afterEach(() => {
    resetHeldComputedDiffs()
  })

  function gitText(text: string) {
    const content = new TextEncoder().encode(text)
    return {
      exists: true,
      content,
      totalSize: BigInt(content.length),
    }
  }

  it('draws its loading state while the diff worker computes, then the diff', async () => {
    readFileImpl.mockResolvedValue(readResp(new TextEncoder().encode('working')))
    readGitFileImpl.mockImplementation(async (...args: unknown[]) => {
      const request = args[1] as { ref: number }
      return request.ref === GitFileRef.HEAD
        ? gitText('kept\n')
        : gitText('kept\nadded line\n')
    })
    holdComputedDiffs(true)
    render(() => (
      <FileViewer
        workerId="w1"
        filePath="/repo/notes.txt"
        fileViewMode="unified-diff"
        fileDiffBase="head-vs-staged"
        gitFileStatus={{ path: 'notes.txt' } as never}
      />
    ))
    await waitFor(() => expect(requestedDiffInputs()).toContainEqual({
      kind: 'texts',
      oldText: 'kept\n',
      newText: 'kept\nadded line\n',
    }))

    expect(screen.getByText('Loading...')).toBeInTheDocument()
    expect(screen.queryByText('added line')).not.toBeInTheDocument()

    holdComputedDiffs(false)

    await waitFor(() => expect(screen.getByText('added line')).toBeInTheDocument())
    expect(screen.queryByText('Loading...')).not.toBeInTheDocument()
  })

  it('asks the diff worker for nothing when a binary file draws its notice', async () => {
    const bytes = new Uint8Array([0, 1, 2, 3, 0, 0, 0])
    statFileImpl.mockResolvedValue(statResp(bytes.length))
    readFileImpl.mockResolvedValue(readResp(bytes))
    readGitFileImpl.mockResolvedValue({
      exists: true,
      content: new Uint8Array([0, 9, 9, 0]),
      totalSize: 4n,
    })
    render(() => (
      <FileViewer
        workerId="w1"
        filePath="/repo/blob.foo"
        fileViewMode="unified-diff"
        fileDiffBase="head-vs-working"
        gitFileStatus={{ path: 'blob.foo', unstagedStatus: GitFileStatusCode.MODIFIED } as never}
      />
    ))
    await waitFor(() => expect(screen.getByText('Binary file blob.foo differs in the working copy.')).toBeInTheDocument())
    await waitFor(() => expect(readGitFileImpl).toHaveBeenCalled())
    await waitFor(() => expect(screen.queryByText('Loading...')).not.toBeInTheDocument())

    expect(requestedDiffInputs()).toEqual([])
  })
})
