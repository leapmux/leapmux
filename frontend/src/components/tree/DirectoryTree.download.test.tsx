import type { SaveActionsSpies } from '../../../tests/unit/helpers/saveActionsMocks'
import { render, screen, waitFor, within } from '@solidjs/testing-library'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import {
  fileDownloadMock,
  platformBridgeMock,
  preferencesMock,
  resetSaveActionsSpies,
  toastMock,
} from '../../../tests/unit/helpers/saveActionsMocks'

// Tree-specific assertions: DirectoryTree wires FileActionsMenu in with
// the `tree` test-id prefix and only shows the file-only items for non-
// directory rows. The full save-action behavior matrix (web vs desktop,
// reveal-after-download, save-as cancellation, etc.) is covered by
// FileActionsMenu.test.tsx — this file would just duplicate that work.

const spies = vi.hoisted<SaveActionsSpies>(() => ({
  downloadImpl: vi.fn(),
  saveAsImpl: vi.fn(),
  saveToDownloadsImpl: vi.fn(),
  revealImpl: vi.fn(),
  warnToastImpl: vi.fn(),
  revealAfterDownloadImpl: vi.fn(() => true),
  isTauriAppImpl: vi.fn(() => false),
}))

const listDirectoryImpl = vi.hoisted(() => vi.fn())

vi.mock('~/api/workerRpc', () => ({
  listDirectory: (...args: unknown[]) => listDirectoryImpl(...args),
  channelManager: { subscribe: () => () => {} },
}))
vi.mock('~/api/platformBridge', () => platformBridgeMock(spies))
vi.mock('~/lib/fileDownload', () => fileDownloadMock(spies))
vi.mock('~/components/common/Toast', () => toastMock(spies))
vi.mock('~/context/PreferencesContext', () => preferencesMock(spies))

const { DirectoryTree } = await import('./DirectoryTree')

function setupTree() {
  listDirectoryImpl.mockResolvedValue({
    entries: [
      { path: '/repo/src', name: 'src', isDir: true, hidden: false },
      { path: '/repo/archive.zip', name: 'archive.zip', isDir: false, hidden: false },
    ],
    truncated: false,
  })

  render(() => (
    <DirectoryTree
      workerId="w1"
      selectedPath="/repo"
      rootPath="/repo"
      homeDir="/home/alice"
      onSelect={() => {}}
      showFiles
    />
  ))
}

async function findRowByText(text: string): Promise<HTMLElement> {
  return await waitFor(() => {
    const rows = screen.getAllByTestId('tree-row')
    const row = rows.find(r => r.textContent?.includes(text))
    if (!row)
      throw new Error(`row with text "${text}" not found`)
    return row
  })
}

beforeEach(() => {
  listDirectoryImpl.mockReset()
  resetSaveActionsSpies(spies)
})

describe('directoryTree context menu — web', () => {
  it('renders the Download item for files but not directories', async () => {
    setupTree()
    const fileRow = await findRowByText('archive.zip')
    expect(within(fileRow).queryByTestId('tree-download-button')).toBeInTheDocument()

    const dirRow = await findRowByText('src')
    expect(within(dirRow).queryByTestId('tree-download-button')).not.toBeInTheDocument()
    // Sanity: the menu rendered for the dir row too (common items present).
    expect(within(dirRow).queryByTestId('tree-copy-path-button')).toBeInTheDocument()
  })
})

describe('directoryTree context menu — desktop', () => {
  beforeEach(() => {
    spies.isTauriAppImpl.mockReturnValue(true)
  })

  it('renders Save as / Save to Downloads for files and neither for directories', async () => {
    setupTree()
    const fileRow = await findRowByText('archive.zip')
    expect(within(fileRow).queryByTestId('tree-save-as-button')).toBeInTheDocument()
    expect(within(fileRow).queryByTestId('tree-save-to-downloads-button')).toBeInTheDocument()
    expect(within(fileRow).queryByTestId('tree-download-button')).not.toBeInTheDocument()

    const dirRow = await findRowByText('src')
    expect(within(dirRow).queryByTestId('tree-save-as-button')).not.toBeInTheDocument()
    expect(within(dirRow).queryByTestId('tree-save-to-downloads-button')).not.toBeInTheDocument()
    expect(within(dirRow).queryByTestId('tree-download-button')).not.toBeInTheDocument()
  })
})
