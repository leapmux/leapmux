import { fireEvent, render, screen, waitFor } from '@solidjs/testing-library'
import { createSignal } from 'solid-js'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { platformBridgeFileSaveStubs } from '../helpers/saveActionsMocks'
import { readResp, statResp } from '../helpers/workerRpcMocks'

const statFileImpl = vi.fn()
const readFileImpl = vi.fn()
const readGitFileImpl = vi.fn(async (..._args: unknown[]) => ({ exists: false, content: new Uint8Array() }))
const downloadImpl = vi.fn((..._args: unknown[]) => Promise.resolve())

vi.mock('~/api/workerRpc', () => ({
  statFile: (...args: unknown[]) => statFileImpl(...args),
  readFile: (...args: unknown[]) => readFileImpl(...args),
  readGitFile: (...args: unknown[]) => readGitFileImpl(...args),
}))

vi.mock('~/lib/fileDownload', () => ({
  downloadFileFromWorker: (...args: unknown[]) => downloadImpl(...args),
  saveFileAs: vi.fn(),
  saveFileToDownloads: vi.fn(),
}))

// Light-weight mocks for the heavy child views so we can assert which
// branch fired without dragging in syntax highlighting / virtualization.
vi.mock('~/components/fileviewer/TextFileView', () => ({
  TextFileView: (props: { filePath: string }) => (
    <div data-testid="text-view">{props.filePath}</div>
  ),
}))
vi.mock('~/components/fileviewer/MarkdownFileView', () => ({
  MarkdownFileView: (props: { filePath: string }) => (
    <div data-testid="markdown-view">{props.filePath}</div>
  ),
}))
vi.mock('~/components/fileviewer/ImageFileView', () => ({
  ImageFileView: (props: { filePath: string }) => (
    <div data-testid="image-view">{props.filePath}</div>
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

const { FileViewer } = await import('~/components/fileviewer/FileViewer')

const MAX = 256 * 1024

describe('fileViewer dispatch with unsupported view', () => {
  beforeEach(() => {
    statFileImpl.mockReset()
    readFileImpl.mockReset()
    downloadImpl.mockReset()
    downloadImpl.mockResolvedValue(undefined)
    showWarnToastMock.mockReset()
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
    const fullSize = 5 * 1024 * 1024
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

  it('renders the unsupported view for a >256 KiB image via metaOnlyIfTruncated (no wasted bytes)', async () => {
    // The image path now collapses statFile + readFile into a single
    // readFile call with metaOnlyIfTruncated=true. For an oversize
    // image, the server skips the bytes and returns just totalSize —
    // we never download the 256 KiB we'd refuse to render anyway.
    readFileImpl.mockResolvedValue(readResp(new Uint8Array(0), 5 * 1024 * 1024))

    render(() => <FileViewer workerId="w1" filePath="/repo/huge.png" />)
    await waitFor(() =>
      expect(screen.getByText('This image is too large to preview.')).toBeInTheDocument(),
    )
    expect(readFileImpl).toHaveBeenCalledTimes(1)
    expect(readFileImpl).toHaveBeenCalledWith('w1', expect.objectContaining({
      path: '/repo/huge.png',
      metaOnlyIfTruncated: true,
    }))
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
    expect(readFileImpl).toHaveBeenCalledWith('w1', expect.objectContaining({
      path: '/repo/archive.zip',
      limit: BigInt(MAX),
    }))
  })

  it('clicking Show anyway on an oversize image lazy-fetches and renders HexView', async () => {
    // First call: initial load with metaOnlyIfTruncated → empty content
    // + oversize totalSize. Second call: Show anyway → partial bytes
    // (the unsupported card lifts metaOnlyIfTruncated to actually fetch).
    const partial = new Uint8Array(1024)
    readFileImpl.mockResolvedValueOnce(readResp(new Uint8Array(0), 5 * 1024 * 1024))
    readFileImpl.mockResolvedValueOnce(readResp(partial, 5 * 1024 * 1024))

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
