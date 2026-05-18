import type { SaveActionsSpies } from '../helpers/saveActionsMocks'
import { fireEvent, render, screen, waitFor } from '@solidjs/testing-library'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import {
  fileDownloadMock,
  platformBridgeMock,
  preferencesMock,
  resetSaveActionsSpies,
  toastMock,
} from '../helpers/saveActionsMocks'

const spies = vi.hoisted<SaveActionsSpies>(() => ({
  downloadImpl: vi.fn(),
  saveAsImpl: vi.fn(),
  saveToDownloadsImpl: vi.fn(),
  revealImpl: vi.fn(),
  warnToastImpl: vi.fn(),
  revealAfterDownloadImpl: vi.fn(() => true),
  isTauriAppImpl: vi.fn(() => false),
}))

vi.mock('~/api/platformBridge', () => platformBridgeMock(spies))
vi.mock('~/lib/fileDownload', () => fileDownloadMock(spies))
vi.mock('~/components/common/Toast', () => toastMock(spies))
vi.mock('~/context/PreferencesContext', () => preferencesMock(spies))

const { FileActionsMenu } = await import('~/components/common/FileActionsMenu')

function renderMenu(overrides: Partial<Parameters<typeof FileActionsMenu>[0]> = {}) {
  const defaults: Parameters<typeof FileActionsMenu>[0] = {
    workerId: 'w1',
    path: '/repo/src/file.ts',
    flavor: 'posix',
    rootPath: '/repo',
    homeDir: '/home/alice',
  }
  // Use a single merged object so explicit `undefined` in overrides
  // actually unsets defaults (Solid's spread keeps earlier values when
  // the new value is undefined).
  const props = { ...defaults, ...overrides }
  return render(() => <FileActionsMenu {...props} />)
}

beforeEach(() => {
  resetSaveActionsSpies(spies)
})

describe('fileActionsMenu — common items', () => {
  it('always shows Copy path; shows Copy relative path when rootPath is supplied', () => {
    renderMenu()
    expect(screen.getByTestId('file-actions-copy-path-button')).toBeInTheDocument()
    expect(screen.getByTestId('file-actions-copy-relative-path-button')).toBeInTheDocument()
  })

  it('hides Copy relative path when rootPath is not supplied', () => {
    renderMenu({ rootPath: undefined })
    expect(screen.queryByTestId('file-actions-copy-relative-path-button')).not.toBeInTheDocument()
  })

  it('shows Mention only when onMention is supplied', () => {
    const { unmount } = renderMenu()
    expect(screen.queryByTestId('file-actions-mention-button')).not.toBeInTheDocument()
    unmount()

    const onMention = vi.fn()
    renderMenu({ onMention })
    fireEvent.click(screen.getByTestId('file-actions-mention-button'))
    expect(onMention).toHaveBeenCalledWith('/repo/src/file.ts')
  })

  it('shows Open terminal only for directories (and with handler)', () => {
    const onOpenTerminal = vi.fn()
    const { unmount } = renderMenu({ isDir: false, onOpenTerminal })
    expect(screen.queryByTestId('file-actions-open-terminal-button')).not.toBeInTheDocument()
    unmount()

    renderMenu({ isDir: true, onOpenTerminal })
    fireEvent.click(screen.getByTestId('file-actions-open-terminal-button'))
    expect(onOpenTerminal).toHaveBeenCalledWith('/repo/src/file.ts')
  })

  it('writes the path to the clipboard on Copy path click', () => {
    const writeText = vi.fn().mockResolvedValue(undefined)
    Object.assign(navigator, { clipboard: { writeText } })
    renderMenu()
    fireEvent.click(screen.getByTestId('file-actions-copy-path-button'))
    expect(writeText).toHaveBeenCalledWith('/repo/src/file.ts')
  })

  it('writes the relative path on Copy relative path click', () => {
    const writeText = vi.fn().mockResolvedValue(undefined)
    Object.assign(navigator, { clipboard: { writeText } })
    renderMenu()
    fireEvent.click(screen.getByTestId('file-actions-copy-relative-path-button'))
    expect(writeText).toHaveBeenCalledWith('src/file.ts')
  })

  it('hides the Download / Save items for directories', () => {
    renderMenu({ isDir: true })
    expect(screen.queryByTestId('file-actions-download-button')).not.toBeInTheDocument()
    expect(screen.queryByTestId('file-actions-save-as-button')).not.toBeInTheDocument()
    expect(screen.queryByTestId('file-actions-save-to-downloads-button')).not.toBeInTheDocument()
  })
})

describe('fileActionsMenu — web mode', () => {
  it('shows a single Download item that calls downloadFileFromWorker', async () => {
    renderMenu()
    const dl = screen.getByTestId('file-actions-download-button')
    expect(dl).toBeInTheDocument()
    expect(screen.queryByTestId('file-actions-save-as-button')).not.toBeInTheDocument()
    expect(screen.queryByTestId('file-actions-save-to-downloads-button')).not.toBeInTheDocument()
    fireEvent.click(dl)
    await waitFor(() => expect(spies.downloadImpl).toHaveBeenCalledWith('w1', '/repo/src/file.ts', 'posix', expect.any(Function)))
  })
})

describe('fileActionsMenu — desktop mode', () => {
  beforeEach(() => {
    spies.isTauriAppImpl.mockReturnValue(true)
  })

  it('shows Save as... and Save to Downloads, hides web Download', () => {
    renderMenu()
    expect(screen.getByTestId('file-actions-save-as-button')).toBeInTheDocument()
    expect(screen.getByTestId('file-actions-save-to-downloads-button')).toBeInTheDocument()
    expect(screen.queryByTestId('file-actions-download-button')).not.toBeInTheDocument()
  })

  it('save to Downloads reveals when the preference is on', async () => {
    renderMenu()
    fireEvent.click(screen.getByTestId('file-actions-save-to-downloads-button'))
    await waitFor(() => expect(spies.saveToDownloadsImpl).toHaveBeenCalledWith('w1', '/repo/src/file.ts', 'posix', expect.any(Function)))
    await waitFor(() => expect(spies.revealImpl).toHaveBeenCalledWith('/home/alice/Downloads/file.ts'))
  })

  it('save as → skips reveal when the user cancels the dialog', async () => {
    spies.saveAsImpl.mockResolvedValueOnce(null)
    renderMenu()
    fireEvent.click(screen.getByTestId('file-actions-save-as-button'))
    await waitFor(() => expect(spies.saveAsImpl).toHaveBeenCalledTimes(1))
    expect(spies.revealImpl).not.toHaveBeenCalled()
  })

  it('save to Downloads does NOT reveal when the preference is off', async () => {
    spies.revealAfterDownloadImpl.mockReturnValue(false)
    renderMenu()
    fireEvent.click(screen.getByTestId('file-actions-save-to-downloads-button'))
    await waitFor(() => expect(spies.saveToDownloadsImpl).toHaveBeenCalledTimes(1))
    expect(spies.revealImpl).not.toHaveBeenCalled()
  })

  it('respects a custom itemTestIdPrefix', () => {
    renderMenu({ itemTestIdPrefix: 'tree' })
    expect(screen.getByTestId('tree-save-as-button')).toBeInTheDocument()
    expect(screen.getByTestId('tree-save-to-downloads-button')).toBeInTheDocument()
    expect(screen.queryByTestId('file-actions-save-as-button')).not.toBeInTheDocument()
  })
})
