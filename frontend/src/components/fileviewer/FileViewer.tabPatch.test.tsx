import { render } from '@solidjs/testing-library'
import { createSignal } from 'solid-js'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { platformBridgeFileSaveStubs } from '~/test-support/saveActionsMocks'
import { readResp } from '~/test-support/workerRpcMocks'
import { FileViewer } from './FileViewer'

// ---------------------------------------------------------------------------
// A patch of the tab that holds the viewer
//
// The tile gives the viewer each prop through a getter over the whole tab object
// (see TileRenderer), and a patch of any field replaces that object. So a patch
// notifies every prop of the viewer again, each with the value that it holds. A
// child view that reads such a prop must not redraw what it already shows.
// ---------------------------------------------------------------------------

/** The first bytes of a PNG file: the viewer reads only its extension and its bytes. */
const PNG_BYTES = new Uint8Array([
  0x89,
  0x50,
  0x4E,
  0x47,
  0x0D,
  0x0A,
  0x1A,
  0x0A,
])

vi.mock('~/api/workerRpc', () => ({
  workerFile: {
    statFile: vi.fn(),
    readFile: vi.fn(async () => readResp(PNG_BYTES)),
  },
  workerGit: {
    readGitFile: vi.fn(),
  },
}))

vi.mock('~/lib/fileDownload', () => ({
  downloadFileFromWorker: vi.fn(),
  saveFileAs: vi.fn(),
  saveFileToDownloads: vi.fn(),
}))

vi.mock('~/context/PreferencesContext', () => ({
  usePreferences: () => ({
    revealAfterDownload: () => false,
    setRevealAfterDownload: () => {},
  }),
}))

vi.mock('~/api/platformBridge', () => ({
  isTauriApp: () => false,
  platformBridge: {
    revealInFileManager: () => Promise.resolve(),
    ...platformBridgeFileSaveStubs(),
  },
}))

describe('FileViewer under a patch of its tab', () => {
  // jsdom implements no blob URL, so each test counts the URLs that the image view creates.
  const createObjectUrl = vi.fn((_blob: Blob) => 'blob:image')
  const revokeObjectUrl = vi.fn((_url: string) => {})
  let originalCreate: typeof URL.createObjectURL
  let originalRevoke: typeof URL.revokeObjectURL

  beforeEach(() => {
    createObjectUrl.mockClear()
    revokeObjectUrl.mockClear()
    originalCreate = URL.createObjectURL
    originalRevoke = URL.revokeObjectURL
    URL.createObjectURL = createObjectUrl
    URL.revokeObjectURL = revokeObjectUrl
  })

  afterEach(() => {
    URL.createObjectURL = originalCreate
    URL.revokeObjectURL = originalRevoke
  })

  it('keeps the image that it shows, and loads it once, when a patch of the tab holds the same file', async () => {
    const tabOf = () => ({
      workerId: 'w1',
      filePath: '/repo/logo.png',
      fileViewMode: 'working' as const,
    })
    const [tab, setTab] = createSignal(tabOf())
    render(() => (
      <FileViewer
        workerId={tab().workerId}
        filePath={tab().filePath}
        fileViewMode={tab().fileViewMode}
      />
    ))
    await vi.waitFor(() => expect(createObjectUrl).toHaveBeenCalledTimes(1))
    expect(createObjectUrl.mock.calls[0]![0].type).toBe('image/png')

    setTab(tabOf())

    expect(createObjectUrl).toHaveBeenCalledTimes(1)
    expect(revokeObjectUrl).not.toHaveBeenCalled()
  })
})
