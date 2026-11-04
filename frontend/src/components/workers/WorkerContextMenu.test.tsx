/// <reference types="vitest/globals" />
import { render, screen, waitFor } from '@solidjs/testing-library'
import { afterEach, beforeAll, describe, expect, it, vi } from 'vitest'
import { WorkerContextMenu } from './WorkerContextMenu'

const showInfoToast = vi.hoisted(() => vi.fn())
vi.mock('~/components/common/Toast', () => ({ showInfoToast }))

const copyTextToClipboard = vi.hoisted(() => vi.fn(async (_text: string) => true))
vi.mock('~/lib/clipboard', () => ({ copyTextToClipboard }))

// Mock the modules that affect visibility.
vi.mock('~/api/platformBridge', async (importOriginal) => {
  const actual = await importOriginal<typeof import('~/api/platformBridge')>()
  return {
    ...actual,
    isTunnelAvailable: vi.fn(() => false),
  }
})

// Stub Popover API for DropdownMenu.
beforeAll(() => {
  HTMLElement.prototype.showPopover = vi.fn()
  HTMLElement.prototype.hidePopover = vi.fn()
  HTMLElement.prototype.togglePopover = vi.fn()
})

function renderMenu(opts?: { hasTunnels?: boolean, autoRegistered?: boolean, online?: boolean }) {
  const onAddTunnel = vi.fn()
  const onDeleteAllTunnels = vi.fn()
  const onDeregister = vi.fn()

  render(() => (
    <WorkerContextMenu
      workerInfo={{
        name: 'test',
        os: 'linux',
        arch: 'amd64',
        homeDir: '/home',
        version: '1.0',
        commitHash: '',
        buildTime: '',
        updatedAt: Date.now(),
      }}
      autoRegistered={opts?.autoRegistered ?? false}
      online={opts?.online ?? true}
      hasTunnels={opts?.hasTunnels ?? false}
      onAddTunnel={onAddTunnel}
      onDeleteAllTunnels={onDeleteAllTunnels}
      onDeregister={onDeregister}
    />
  ))

  // Open the dropdown.
  const trigger = screen.getByRole('button')
  trigger.click()

  return { onAddTunnel, onDeleteAllTunnels, onDeregister }
}

describe('WorkerContextMenu', () => {
  afterEach(() => {
    vi.restoreAllMocks()
    showInfoToast.mockClear()
    copyTextToClipboard.mockClear()
    copyTextToClipboard.mockImplementation(async () => true)
  })

  it('"add tunnel..." hidden when tunnel not available', async () => {
    const { isTunnelAvailable } = await import('~/api/platformBridge')
    vi.mocked(isTunnelAvailable).mockReturnValue(false)

    renderMenu()
    expect(screen.queryByText('Add tunnel...')).not.toBeInTheDocument()
  })

  it('"add tunnel..." visible when tunnels are available', async () => {
    const { isTunnelAvailable } = await import('~/api/platformBridge')
    vi.mocked(isTunnelAvailable).mockReturnValue(true)

    renderMenu()
    expect(screen.getByText('Add tunnel...')).toBeInTheDocument()
  })

  it('clicking "add tunnel..." calls onAddTunnel', async () => {
    const { isTunnelAvailable } = await import('~/api/platformBridge')
    vi.mocked(isTunnelAvailable).mockReturnValue(true)

    const { onAddTunnel } = renderMenu()
    screen.getByText('Add tunnel...').click()
    expect(onAddTunnel).toHaveBeenCalled()
  })

  it.each([
    ['online', true],
    ['offline', false],
  ])('"deregister..." visible for a manually-registered %s worker', (_label, online) => {
    renderMenu({ autoRegistered: false, online })
    expect(screen.getByText('Deregister...')).toBeInTheDocument()
  })

  it('"deregister..." hidden for a connected bundled worker', () => {
    // The hub refuses this request while the bundled worker is connected, so
    // the menu item would always fail.
    renderMenu({ autoRegistered: true, online: true })
    expect(screen.queryByText('Deregister...')).not.toBeInTheDocument()
  })

  it('"deregister..." visible for an offline bundled worker', () => {
    // An orphan from a lost solo state file stays offline for ever, and this
    // item is the only way that the browser can remove it.
    renderMenu({ autoRegistered: true, online: false })
    expect(screen.getByText('Deregister...')).toBeInTheDocument()
  })

  describe('copying worker info', () => {
    it('copies the info JSON and confirms', async () => {
      renderMenu()
      screen.getByText('linux (amd64)').click()

      await waitFor(() => expect(copyTextToClipboard).toHaveBeenCalled())
      expect(copyTextToClipboard.mock.calls[0]?.[0]).toContain('linux')
      expect(showInfoToast).toHaveBeenCalledWith('Worker info copied to clipboard')
    })

    // The toast used to fire unconditionally, so it claimed success on any
    // platform without a clipboard (a non-secure origin exposes none) and even
    // when there was nothing to copy at all.
    it('stays silent when the copy does not land', async () => {
      copyTextToClipboard.mockImplementation(async () => false)
      renderMenu()
      screen.getByText('linux (amd64)').click()

      await waitFor(() => expect(copyTextToClipboard).toHaveBeenCalled())
      expect(showInfoToast).not.toHaveBeenCalled()
    })
  })
})
