import { fireEvent, render, screen, waitFor } from '@solidjs/testing-library'
import { createSignal } from 'solid-js'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { DIFF_DRAWN_LINE_LIMIT } from '~/components/chat/diff/diffBuilder'
import { LINE_DIFF_MAX_EDITS } from '~/components/chat/diff/diffCompute'
import { diffAdded, diffAddedInline, diffContent, diffRemoved } from '~/components/chat/diff/diffStyles.css'
import { VERSION_DIFF_MAX_EDITS } from '~/components/fileviewer/versionDiff'
import { FILE_READ_LIMIT_BYTES } from '~/generated/contracts/file-read'
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

const MAX = FILE_READ_LIMIT_BYTES
/** The read limit as the status bar and the diff notices state it. */
const CUT_SIZE = '256.0 KB'

// The log of diff requests belongs to the whole file: a test in any describe can
// fill it, so each test starts with an empty one.
beforeEach(() => {
  resetHeldComputedDiffs()
})

type GitRead = Awaited<ReturnType<typeof readGitFileImpl>>

/** A whole git version of `text`, or the first `content` of a longer one when `totalSize` exceeds it. */
function gitRead(text: string, totalSize?: number): GitRead {
  const content = new TextEncoder().encode(text)
  return {
    exists: true,
    content,
    totalSize: BigInt(totalSize ?? content.length),
  }
}

/** A git read of `text` that the worker cut at the limit, as the worker answers it. */
function cutGitRead(text: string): GitRead {
  const bytes = new TextEncoder().encode(text)
  return {
    exists: true,
    content: bytes.subarray(0, MAX),
    totalSize: BigInt(bytes.length),
  }
}

/** A read of the working copy `text` that the worker cut at the limit. */
function cutWorkingRead(text: string) {
  const bytes = new TextEncoder().encode(text)
  return readResp(bytes.subarray(0, MAX), bytes.length)
}

/** The lines of a tracked file of 280,000 bytes: 8,000 lines of 35 bytes each, with the newline. */
const LONG_FILE_LINES = 8000

/** Lines `from` to `to - 1` of the long file, without their newlines. */
function longFileLines(from: number, to: number): string[] {
  return Array.from({ length: to - from }, (_, i) => `line ${String(from + i).padStart(6, '0')} of a long tracked file`)
}

/** Lines `from` to `to - 1` of the long file, each with its newline. */
function longFile(from = 0, to = LONG_FILE_LINES): string {
  return longFileLines(from, to).map(line => `${line}\n`).join('')
}

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

  /** A git read that the test answers by hand. */
  function deferredGitRead() {
    let resolve!: (value: GitRead) => void
    const promise = new Promise<GitRead>((resolvePromise) => {
      resolve = resolvePromise
    })
    return { promise, resolve }
  }

  it('keeps the staged copy when the earlier HEAD read answers last', async () => {
    const head = deferredGitRead()
    readGitFileImpl.mockImplementationOnce(() => head.promise)
      .mockResolvedValue(gitRead('staged text'))
    const [mode, setMode] = createSignal<'head' | 'staged'>('head')
    render(() => <FileViewer workerId="w1" filePath="/repo/notes.txt" fileViewMode={mode()} />)
    await waitFor(() => expect(readGitFileImpl).toHaveBeenCalledOnce())

    setMode('staged')
    await waitFor(() => expect(screen.getByTestId('text-view')).toHaveAttribute('data-text', 'staged text'))
    head.resolve(gitRead('head text'))
    await new Promise(resolve => setTimeout(resolve, 0))

    expect(screen.getByTestId('text-view')).toHaveAttribute('data-text', 'staged text')
  })

  it('aborts the git read of a superseded mode', async () => {
    const head = deferredGitRead()
    readGitFileImpl.mockImplementationOnce(() => head.promise)
      .mockResolvedValue(gitRead('staged text'))
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

  // Working mode reads nothing, and the read that the switch abandoned answers
  // for a mode that is no longer shown. The loading state must end at the switch
  // itself.
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
    head.resolve(gitRead('head text'))
    await new Promise(resolve => setTimeout(resolve, 0))
    expect(screen.getByText('Loading...'), 'the late answer of the older run must not clear the flag').toBeInTheDocument()

    staged.resolve(gitRead('staged text'))
    await waitFor(() => expect(screen.getByTestId('text-view')).toHaveAttribute('data-text', 'staged text'))
    expect(screen.queryByText('Loading...')).not.toBeInTheDocument()
  })

  // A failed read is not a missing file: the pane states the failure.
  it.each([
    ['a ref mode', 'head', {}],
    [
      'a diff mode',
      'unified-diff',
      { fileDiffBase: 'head-vs-staged' as const },
    ],
  ] as const)('states the failure of a git read in %s', async (_label, mode, extraProps) => {
    readGitFileImpl.mockRejectedValue(new Error('git is not installed'))
    render(() => (
      <FileViewer
        workerId="w1"
        filePath="/repo/notes.txt"
        fileViewMode={mode}
        gitFileStatus={{ path: 'notes.txt' } as never}
        {...extraProps}
      />
    ))

    await waitFor(() => expect(screen.getByText('git is not installed')).toBeInTheDocument())
    expect(screen.queryByText('File does not exist at this ref')).not.toBeInTheDocument()
    expect(screen.queryByText('Loading...')).not.toBeInTheDocument()
  })

  // Tab rehydration mounts the viewer with an empty path before the real one
  // arrives, as the working-copy load notes. A read of the empty path fails, and
  // the pane would state that failure until the real path arrives.
  it('reads no git version while the path is empty', async () => {
    readGitFileImpl.mockResolvedValue(gitRead('head text'))
    const [path, setPath] = createSignal('')
    render(() => <FileViewer workerId="w1" filePath={path()} fileViewMode="head" />)
    await new Promise(resolve => setTimeout(resolve, 0))

    expect(readGitFileImpl).not.toHaveBeenCalled()
    expect(screen.getByText('Loading...')).toBeInTheDocument()

    setPath('/repo/notes.txt')

    await waitFor(() => expect(screen.getByTestId('text-view')).toHaveAttribute('data-text', 'head text'))
    expect(readGitFileImpl).toHaveBeenCalledOnce()
  })
})

// The HEAD and staged versions stop where the working copy does, at 256 KiB, and
// the status bar says so for the version that the current mode shows.
describe('FileViewer truncated git versions', () => {
  const TRUNCATED = /Truncated at 256(\.0)? KB/i

  beforeEach(() => {
    statFileImpl.mockReset()
    readFileImpl.mockReset()
    readGitFileImpl.mockReset()
    readFileImpl.mockResolvedValue(readResp(new TextEncoder().encode('working')))
  })

  it('reads a git version up to the limit of the working copy', async () => {
    readGitFileImpl.mockResolvedValue(gitRead('head text', 9))
    render(() => <FileViewer workerId="w1" filePath="/repo/notes.txt" fileViewMode="head" />)
    await waitFor(() => expect(screen.getByTestId('text-view')).toHaveAttribute('data-text', 'head text'))

    expect(readGitFileImpl).toHaveBeenCalledWith('w1', expect.objectContaining({
      path: '/repo/notes.txt',
      limit: BigInt(MAX),
    }), expect.anything())
  })

  it('warns that the HEAD version shown is truncated', async () => {
    readGitFileImpl.mockResolvedValue(gitRead('head text', MAX + 1))
    render(() => <FileViewer workerId="w1" filePath="/repo/notes.txt" fileViewMode="head" />)
    await waitFor(() => expect(screen.getByTestId('text-view')).toHaveAttribute('data-text', 'head text'))

    expect(screen.getByText(TRUNCATED)).toBeInTheDocument()
  })

  it('does not warn for a whole HEAD version, even when the working copy is truncated', async () => {
    readFileImpl.mockResolvedValue(readResp(new TextEncoder().encode('working'), MAX + 1))
    readGitFileImpl.mockResolvedValue(gitRead('head text', 9))
    render(() => <FileViewer workerId="w1" filePath="/repo/notes.txt" fileViewMode="head" />)
    await waitFor(() => expect(screen.getByTestId('text-view')).toHaveAttribute('data-text', 'head text'))

    expect(screen.queryByText(TRUNCATED)).not.toBeInTheDocument()
  })

  it.each([
    [
      'the HEAD side',
      'head-vs-staged',
      [MAX + 1, 6],
      7,
    ],
    [
      'the staged side',
      'head-vs-staged',
      [4, MAX + 1],
      7,
    ],
    [
      'the working side',
      'head-vs-working',
      [4, 4],
      MAX + 1,
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

  // The status bar of a ref or diff mode states only a truncation. The return to
  // the download view acts on working mode, so it shows only there.
  it('offers the return to the download view in working mode only', async () => {
    statFileImpl.mockResolvedValue(statResp(MAX + 1))
    readFileImpl.mockResolvedValue(readResp(new Uint8Array(MAX), MAX + 1))
    readGitFileImpl.mockResolvedValue({
      exists: true,
      content: new Uint8Array(MAX),
      totalSize: BigInt(MAX + 1),
    })
    const [mode, setMode] = createSignal<'working' | 'head'>('working')
    render(() => <FileViewer workerId="w1" filePath="/repo/archive.zip" fileViewMode={mode()} />)
    await waitFor(() => expect(screen.getByTestId('unsupported-show-anyway-button')).toBeInTheDocument())
    fireEvent.click(screen.getByTestId('unsupported-show-anyway-button'))
    await waitFor(() => expect(screen.getByTestId('file-show-download-view-button')).toBeInTheDocument())

    setMode('head')
    await waitFor(() => expect(screen.getByTestId('text-view')).toBeInTheDocument())

    expect(screen.getByText(TRUNCATED)).toBeInTheDocument()
    expect(screen.queryByTestId('file-show-download-view-button')).not.toBeInTheDocument()

    setMode('working')

    await waitFor(() => expect(screen.getByTestId('file-show-download-view-button')).toBeInTheDocument())
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

  it('draws its loading state while the diff worker computes, then the diff', async () => {
    readFileImpl.mockResolvedValue(readResp(new TextEncoder().encode('working')))
    readGitFileImpl.mockImplementation(async (...args: unknown[]) => {
      const request = args[1] as { ref: number }
      return request.ref === GitFileRef.HEAD
        ? gitRead('kept\n')
        : gitRead('kept\nadded line\n')
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
      drawnLineLimit: Number.POSITIVE_INFINITY,
      maxLineEdits: VERSION_DIFF_MAX_EDITS,
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

  // The indicator of a binary file states its git status, which needs no git
  // read, so the failure of that read is not news to the reader.
  it('states the git status of a binary file that the content marks, and not the failure of its git read', async () => {
    const bytes = new Uint8Array([0, 1, 2, 3, 0, 0, 0])
    statFileImpl.mockResolvedValue(statResp(bytes.length))
    readFileImpl.mockResolvedValue(readResp(bytes))
    readGitFileImpl.mockRejectedValue(new Error('git is not installed'))
    render(() => (
      <FileViewer
        workerId="w1"
        filePath="/repo/blob.foo"
        fileViewMode="unified-diff"
        fileDiffBase="head-vs-working"
        gitFileStatus={{ path: 'blob.foo', unstagedStatus: GitFileStatusCode.MODIFIED } as never}
      />
    ))
    await waitFor(() => expect(readGitFileImpl).toHaveBeenCalled())
    await waitFor(() => expect(screen.queryByText('Loading...')).not.toBeInTheDocument())

    expect(screen.getByTestId('binary-diff-indicator')).toHaveTextContent('Binary file blob.foo differs in the working copy.')
    expect(screen.queryByText('git is not installed')).not.toBeInTheDocument()
    expect(requestedDiffInputs()).toEqual([])
  })

  // The index can delete a file that HEAD holds, and the staged side then has no
  // version. The diff compares HEAD with an empty file.
  it('removes every HEAD line in a diff against a staged copy that the index deleted', async () => {
    readFileImpl.mockResolvedValue(readResp(new TextEncoder().encode('working')))
    readGitFileImpl.mockImplementation(async (...args: unknown[]) => {
      const request = args[1] as { ref: number }
      return request.ref === GitFileRef.HEAD
        ? gitRead('first line\nsecond line\n')
        : {
            exists: false,
            content: new Uint8Array(),
            totalSize: 0n,
          }
    })
    const { container } = render(() => (
      <FileViewer
        workerId="w1"
        filePath="/repo/notes.txt"
        fileViewMode="unified-diff"
        fileDiffBase="head-vs-staged"
        gitFileStatus={{ path: 'notes.txt', stagedStatus: GitFileStatusCode.DELETED } as never}
      />
    ))
    await waitFor(() => expect(container.querySelector('[data-file-diff]')).toBeInTheDocument())

    /** The text of each drawn line of the given kind. */
    const drawnTexts = (kind: string): Array<string | null> => [
      ...container.querySelectorAll(`.${kind} .${diffContent}`),
    ].map(content => content.textContent)
    expect(drawnTexts(diffRemoved)).toEqual(['first line', 'second line'])
    expect(drawnTexts(diffAdded)).toEqual([])
    expect(screen.queryByTestId('diff-replacement-notice')).not.toBeInTheDocument()
    expect(requestedDiffInputs()).toContainEqual(expect.objectContaining({
      kind: 'texts',
      oldText: 'first line\nsecond line\n',
      newText: '',
    }))
  })

  // The file viewer mounts one copy of a diff, so it draws the whole diff: the
  // line limit of a chat row, which mounts two copies, does not apply.
  it('draws every line of a diff longer than the line limit of a chat row', async () => {
    const lines = Array.from({ length: DIFF_DRAWN_LINE_LIMIT + 500 }, (_, i) => `added row ${i}`)
    readFileImpl.mockResolvedValue(readResp(new TextEncoder().encode('working')))
    readGitFileImpl.mockImplementation(async (...args: unknown[]) => {
      const request = args[1] as { ref: number }
      return request.ref === GitFileRef.HEAD
        ? {
            exists: false,
            content: new Uint8Array(),
            totalSize: 0n,
          }
        : gitRead(`${lines.join('\n')}\n`)
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

    await waitFor(() => expect(screen.getByText('added row 0')).toBeInTheDocument())

    expect(screen.queryByTestId('diff-truncation-notice')).not.toBeInTheDocument()
    expect(screen.getByText(`added row ${DIFF_DRAWN_LINE_LIMIT + 499}`)).toBeInTheDocument()
  })

  it('highlights the changed word of a line past the line limit of a chat row', async () => {
    // One word changes on every sixth line. The five unchanged lines between two
    // changes join them into one hunk, so the diff has more lines than the limit
    // of a chat row while it needs far fewer line edits than LINE_DIFF_MAX_EDITS.
    const lineCount = DIFF_DRAWN_LINE_LIMIT + 400
    const lastChanged = lineCount - lineCount % 6
    const version = (word: string) => Array.from({ length: lineCount + 1 }, (_, i) => i % 6 === 0
      ? `line ${i} says ${word}\n`
      : `line ${i} stays\n`).join('')
    readFileImpl.mockResolvedValue(readResp(new TextEncoder().encode('working')))
    readGitFileImpl.mockImplementation(async (...args: unknown[]) => {
      const request = args[1] as { ref: number }
      return request.ref === GitFileRef.HEAD
        ? gitRead(version('old'))
        : gitRead(version('new'))
    })
    const { container } = render(() => (
      <FileViewer
        workerId="w1"
        filePath="/repo/notes.txt"
        fileViewMode="unified-diff"
        fileDiffBase="head-vs-staged"
        gitFileStatus={{ path: 'notes.txt' } as never}
      />
    ))
    await waitFor(() => expect(container.querySelector('[data-file-diff]')?.textContent).toContain(`line ${lastChanged} says new`))

    expect(screen.queryByTestId('diff-replacement-notice')).not.toBeInTheDocument()
    /** The highlighted words of the drawn added line `text`. */
    const highlightedWords = (text: string): Array<string | null> => {
      const added = [
        ...container.querySelectorAll(`.${diffAdded} .${diffContent}`),
      ].filter(content => content.textContent === text)
      expect(added, text).toHaveLength(1)
      return [
        ...added[0]!.querySelectorAll(`.${diffAddedInline}`),
      ].map(span => span.textContent)
    }
    expect(highlightedWords('line 0 says new')).toEqual(['new'])
    expect(highlightedWords(`line ${lastChanged} says new`)).toEqual(['new'])
  })

  it('draws the minimal diff of a re-indent past the line edit limit of a chat row', async () => {
    // Each re-indented line is one removal and one addition, so the diff needs
    // twice as many line edits as LINE_DIFF_MAX_EDITS.
    const lineCount = LINE_DIFF_MAX_EDITS
    expect(2 * lineCount).toBeLessThanOrEqual(VERSION_DIFF_MAX_EDITS)
    const version = (indent: string) => [
      'head',
      ...Array.from({ length: lineCount }, (_, i) => `${indent}line ${i}`),
      'tail',
      '',
    ].join('\n')
    readFileImpl.mockResolvedValue(readResp(new TextEncoder().encode('working')))
    readGitFileImpl.mockImplementation(async (...args: unknown[]) => {
      const request = args[1] as { ref: number }
      return request.ref === GitFileRef.HEAD
        ? gitRead(version(''))
        : gitRead(version('  '))
    })
    const { container } = render(() => (
      <FileViewer
        workerId="w1"
        filePath="/repo/notes.txt"
        fileViewMode="unified-diff"
        fileDiffBase="head-vs-staged"
        gitFileStatus={{ path: 'notes.txt' } as never}
      />
    ))
    await waitFor(() => expect(container.querySelector('[data-file-diff]')?.textContent).toContain(`line ${lineCount - 1}`))

    expect(screen.queryByTestId('diff-replacement-notice')).not.toBeInTheDocument()
    /** The text of each drawn line of the given kind. */
    const drawnTexts = (kind: string): Array<string | null> => [
      ...container.querySelectorAll(`.${kind} .${diffContent}`),
    ].map(content => content.textContent)
    // The unchanged first and last lines draw as context, not as changed lines.
    expect(drawnTexts(diffRemoved)).not.toContain('head')
    expect(drawnTexts(diffAdded)).not.toContain('tail')
    expect(drawnTexts(diffAdded)).toContain(`  line ${lineCount - 1}`)
    // A paired line highlights its new indent.
    const lastAdded = [
      ...container.querySelectorAll(`.${diffAdded} .${diffContent}`),
    ].find(content => content.textContent === `  line ${lineCount - 1}`)
    expect([
      ...lastAdded!.querySelectorAll(`.${diffAddedInline}`),
    ].map(span => span.textContent)).toEqual(['  '])
  })

  describe('a switch between git modes', () => {
    /** Answer each git read with a text that states its file and its ref, and the working copy with one text. */
    function readsByRef() {
      readFileImpl.mockResolvedValue(readResp(new TextEncoder().encode('working copy text\n')))
      readGitFileImpl.mockImplementation(async (...args: unknown[]) => {
        const request = args[1] as { ref: number, path: string }
        const name = request.path.endsWith('b.txt')
          ? 'b'
          : 'a'
        return request.ref === GitFileRef.HEAD
          ? gitRead(`${name} head\n`)
          : gitRead(`${name} staged\n`)
      })
    }

    /** The text of the diff that the viewer draws, or the empty string when it draws none. */
    function drawnDiffText(container: HTMLElement): string {
      return container.querySelector('[data-file-diff]')?.textContent ?? ''
    }

    it('asks for no diff of the staged copy beside the working copy that an earlier diff read', async () => {
      readsByRef()
      const [mode, setMode] = createSignal<'unified-diff' | 'staged'>('unified-diff')
      const { container } = render(() => (
        <FileViewer
          workerId="w1"
          filePath="/repo/a.txt"
          fileViewMode={mode()}
          fileDiffBase="head-vs-working"
          gitFileStatus={{ path: 'a.txt' } as never}
        />
      ))
      await waitFor(() => expect(drawnDiffText(container)).toContain('working copy text'))
      setMode('staged')
      await waitFor(() => expect(screen.getByTestId('text-view')).toHaveAttribute('data-text', 'a staged\n'))

      setMode('unified-diff')
      await waitFor(() => expect(drawnDiffText(container)).toContain('working copy text'))

      expect(requestedDiffInputs()).not.toContainEqual(expect.objectContaining({ oldText: 'a staged\n' }))
    })

    it('asks for no diff of the new path\'s HEAD version beside the old path\'s working copy', async () => {
      readsByRef()
      const [path, setPath] = createSignal('/repo/a.txt')
      const { container } = render(() => (
        <FileViewer
          workerId="w1"
          filePath={path()}
          fileViewMode="unified-diff"
          fileDiffBase="head-vs-staged"
          gitFileStatus={{ path: 'a.txt' } as never}
        />
      ))
      await waitFor(() => expect(drawnDiffText(container)).toContain('a staged'))

      setPath('/repo/b.txt')
      await waitFor(() => expect(drawnDiffText(container)).toContain('b staged'))

      const versionsOfOnePath = [
        {
          kind: 'texts',
          oldText: 'a head\n',
          newText: 'a staged\n',
          drawnLineLimit: Number.POSITIVE_INFINITY,
          maxLineEdits: VERSION_DIFF_MAX_EDITS,
        },
        {
          kind: 'texts',
          oldText: 'b head\n',
          newText: 'b staged\n',
          drawnLineLimit: Number.POSITIVE_INFINITY,
          maxLineEdits: VERSION_DIFF_MAX_EDITS,
        },
      ]
      // A viewer that waits for its diff reads it again at each reply, so an
      // input can repeat. No input may pair the versions of two paths.
      expect(requestedDiffInputs()).toEqual(expect.arrayContaining(versionsOfOnePath))
      for (const input of requestedDiffInputs())
        expect(versionsOfOnePath).toContainEqual(input)
    })
  })
})

// A mode-only change, such as `chmod +x`, leaves two versions with the same
// text, and so does a change past the read limit. The pane says so instead of
// drawing nothing.
describe('FileViewer equal versions', () => {
  beforeEach(() => {
    statFileImpl.mockReset()
    readFileImpl.mockReset()
    readGitFileImpl.mockReset()
  })

  it('states that two whole versions have the same text', async () => {
    readFileImpl.mockResolvedValue(readResp(new TextEncoder().encode('same\n')))
    readGitFileImpl.mockResolvedValue(gitRead('same\n'))
    render(() => (
      <FileViewer
        workerId="w1"
        filePath="/repo/script.sh"
        fileViewMode="unified-diff"
        fileDiffBase="head-vs-working"
        gitFileStatus={{ path: 'script.sh', unstagedStatus: GitFileStatusCode.MODIFIED } as never}
      />
    ))

    await waitFor(() => expect(screen.getByText('The two versions have the same text.')).toBeInTheDocument())
    expect(screen.queryByText('Loading...')).not.toBeInTheDocument()
  })

  it('states that two cut versions have the same text up to where the viewer stops reading', async () => {
    const changedPastTheCut = longFile().replace('line 007900 of a long tracked file', 'line 007900 was changed')
    readFileImpl.mockResolvedValue(cutWorkingRead(changedPastTheCut))
    readGitFileImpl.mockResolvedValue(cutGitRead(longFile()))
    render(() => (
      <FileViewer
        workerId="w1"
        filePath="/repo/long.txt"
        fileViewMode="split-diff"
        fileDiffBase="head-vs-working"
        gitFileStatus={{ path: 'long.txt', unstagedStatus: GitFileStatusCode.MODIFIED } as never}
      />
    ))

    await waitFor(() => expect(screen.getByText(
      `No changes up to where the viewer stops reading one of the versions. It reads only the first ${CUT_SIZE} of each version.`,
    )).toBeInTheDocument())
    expect(screen.queryByText('The two versions have the same text.')).not.toBeInTheDocument()
  })
})

// The worker cuts each git read at 256 KiB, and the viewer reads 256 KiB of the
// working copy. A change that moves bytes before the cut makes the two reads end
// at different lines, so the end of each read is not a change of the file. The
// diff stops where the viewer stops reading one of the versions, and says so.
describe('FileViewer diff of versions past the read limit', () => {
  beforeEach(() => {
    statFileImpl.mockReset()
    readFileImpl.mockReset()
    readGitFileImpl.mockReset()
  })

  const STOP_NOTICE = `The diff stops where the viewer stops reading one of the versions. It reads only the first ${CUT_SIZE} of each version.`

  /** Render the HEAD-vs-working diff of the two reads, and list the lines it draws as removed and as added. */
  async function drawnWorkingDiff(path: string, headRead: GitRead, workingRead: ReturnType<typeof readResp>) {
    readGitFileImpl.mockResolvedValue(headRead)
    readFileImpl.mockResolvedValue(workingRead)
    const { container } = render(() => (
      <FileViewer
        workerId="w1"
        filePath={path}
        fileViewMode="unified-diff"
        fileDiffBase="head-vs-working"
        gitFileStatus={{
          path: path.slice('/repo/'.length),
          unstagedStatus: GitFileStatusCode.MODIFIED,
        } as never}
      />
    ))
    await waitFor(() => expect(container.querySelector('[data-file-diff]')).not.toBeNull())
    const linesOf = (lineClass: string) => [
      ...container.querySelectorAll(`.${lineClass}`),
    ]
      .map(line => line.querySelector(`.${diffContent}`)?.textContent ?? '')
    return {
      container,
      removed: linesOf(diffRemoved),
      added: linesOf(diffAdded),
    }
  }

  it('draws no change at the end of two reads that a shorter first line moves apart', async () => {
    const working = longFile().replace('line 000000 of a long tracked file', 'changed first line')
    const { removed, added } = await drawnWorkingDiff('/repo/long.txt', cutGitRead(longFile()), cutWorkingRead(working))

    expect(removed).toEqual(['line 000000 of a long tracked file'])
    expect(added).toEqual(['changed first line'])
    expect(screen.getByText(STOP_NOTICE)).toBeInTheDocument()
  })

  it('draws the lines inserted before the cut, and no removal of the HEAD lines that the working read leaves out', async () => {
    const inserted = Array.from({ length: 100 }, (_, i) => `inserted line ${String(i).padStart(3, '0')}`)
    const working = longFile(0, 10) + inserted.map(line => `${line}\n`).join('') + longFile(10, LONG_FILE_LINES)
    const { removed, added } = await drawnWorkingDiff('/repo/long.txt', cutGitRead(longFile()), cutWorkingRead(working))

    expect(added).toEqual(inserted)
    expect(removed).toEqual([])
  })

  it('draws the lines deleted before the cut, and no addition of the working lines that the HEAD read leaves out', async () => {
    const working = longFile(0, 10) + longFile(110, LONG_FILE_LINES)
    const { removed, added } = await drawnWorkingDiff('/repo/long.txt', cutGitRead(longFile()), cutWorkingRead(working))

    expect(removed).toEqual(longFileLines(10, 110))
    expect(added).toEqual([])
  })

  it('draws no replacement character for a cut inside a multi-byte UTF-8 character', async () => {
    const body = `${'ü'.repeat(16)}\n`.repeat(8000)
    // The HEAD read ends one byte into a two-byte character; the working read
    // ends between two characters.
    const { container, removed, added } = await drawnWorkingDiff('/repo/umlauts.txt', cutGitRead(`x\n${body}`), cutWorkingRead(`yy\n${body}`))

    expect(removed).toEqual(['x'])
    expect(added).toEqual(['yy'])
    expect(container.textContent).not.toContain('�')
  })

  it('keeps the removal of the lines that a shorter working copy lost', async () => {
    // The working copy is whole: it ends 89 lines before the HEAD read does.
    const working = longFile(0, 7400)
    const { removed, added } = await drawnWorkingDiff('/repo/long.txt', cutGitRead(longFile()), readResp(new TextEncoder().encode(working)))

    expect(removed).toEqual(longFileLines(7400, 7489))
    expect(added).toEqual([])
    expect(screen.getByText(STOP_NOTICE)).toBeInTheDocument()
  })

  it('compares an image\'s working copy only as far as its HEAD version is read', async () => {
    // An SVG working copy loads up to the image limit, far past the git read limit.
    const working = longFile().replace('line 000005 of a long tracked file', 'line 000005 was changed')
    const { removed, added } = await drawnWorkingDiff('/repo/icon.svg', cutGitRead(longFile()), readResp(new TextEncoder().encode(working)))

    expect(removed).toEqual(['line 000005 of a long tracked file'])
    expect(added).toEqual(['line 000005 was changed'])
    expect(screen.getByText(STOP_NOTICE)).toBeInTheDocument()
  })

  it('draws the change of a file that is one line longer than the read limit', async () => {
    const tail = 'x'.repeat(MAX + 10)
    const { removed, added } = await drawnWorkingDiff('/repo/bundle.min.js', cutGitRead(`a${tail}`), cutWorkingRead(`b${tail}`))

    expect(removed).toHaveLength(1)
    expect(added).toHaveLength(1)
    expect(removed[0]?.startsWith('ax')).toBe(true)
    expect(added[0]?.startsWith('bx')).toBe(true)
  })

  it('draws a change at the end of two whole versions, with no notice', async () => {
    const { removed, added } = await drawnWorkingDiff(
      '/repo/notes.txt',
      gitRead('a\nb\nc\n'),
      readResp(new TextEncoder().encode('a\nb\nc\nd\n')),
    )

    expect(removed).toEqual([])
    expect(added).toEqual(['d'])
    expect(screen.queryByText(STOP_NOTICE)).not.toBeInTheDocument()
    expect(screen.queryByText(/Truncated at/i)).not.toBeInTheDocument()
  })
})
