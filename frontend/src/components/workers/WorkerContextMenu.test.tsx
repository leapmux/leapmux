/// <reference types="vitest/globals" />
import { render, screen } from '@solidjs/testing-library'
import { afterEach, beforeAll, describe, expect, it, vi } from 'vitest'
import { WorkerContextMenu } from './WorkerContextMenu'

// Mock the modules that affect visibility.
vi.mock('~/lib/systemInfo', () => ({
  isSoloMode: vi.fn(() => true),
}))

vi.mock('~/api/tunnelApi', () => ({
  isTunnelAvailable: vi.fn(() => false),
}))

// Stub Popover API for DropdownMenu.
beforeAll(() => {
  HTMLElement.prototype.showPopover = vi.fn()
  HTMLElement.prototype.hidePopover = vi.fn()
  HTMLElement.prototype.togglePopover = vi.fn()
})

function renderMenu(opts?: { isOwner?: boolean, hasTunnels?: boolean }) {
  const onAddTunnel = vi.fn()
  const onDeleteAllTunnels = vi.fn()
  const onDeregister = vi.fn()

  render(() => (
    <WorkerContextMenu
      workerInfo={{ name: 'test', os: 'linux', arch: 'amd64', homeDir: '/home', version: '1.0', updatedAt: Date.now() }}
      isOwner={opts?.isOwner ?? true}
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

describe('workerContextMenu', () => {
  afterEach(() => {
    vi.restoreAllMocks()
  })

  it('"add tunnel..." hidden when tunnel not available', async () => {
    const { isTunnelAvailable } = await import('~/api/tunnelApi')
    vi.mocked(isTunnelAvailable).mockReturnValue(false)

    renderMenu()
    expect(screen.queryByText('Add tunnel...')).not.toBeInTheDocument()
  })

  it('"add tunnel..." hidden when not owner', async () => {
    const { isTunnelAvailable } = await import('~/api/tunnelApi')
    vi.mocked(isTunnelAvailable).mockReturnValue(true)

    renderMenu({ isOwner: false })
    expect(screen.queryByText('Add tunnel...')).not.toBeInTheDocument()
  })

  it('"add tunnel..." visible when available + owner', async () => {
    const { isTunnelAvailable } = await import('~/api/tunnelApi')
    vi.mocked(isTunnelAvailable).mockReturnValue(true)

    renderMenu({ isOwner: true })
    expect(screen.getByText('Add tunnel...')).toBeInTheDocument()
  })

  it('clicking "add tunnel..." calls onAddTunnel', async () => {
    const { isTunnelAvailable } = await import('~/api/tunnelApi')
    vi.mocked(isTunnelAvailable).mockReturnValue(true)

    const { onAddTunnel } = renderMenu({ isOwner: true })
    screen.getByText('Add tunnel...').click()
    expect(onAddTunnel).toHaveBeenCalled()
  })
})
