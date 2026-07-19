/// <reference types="vitest/globals" />
import { render, screen } from '@solidjs/testing-library'
import { afterEach, beforeAll, describe, expect, it, vi } from 'vitest'
import { WorkerContextMenu } from './WorkerContextMenu'

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

function renderMenu(opts?: { hasTunnels?: boolean, autoRegistered?: boolean }) {
  const onAddTunnel = vi.fn()
  const onDeleteAllTunnels = vi.fn()
  const onDeregister = vi.fn()

  render(() => (
    <WorkerContextMenu
      workerInfo={{ name: 'test', os: 'linux', arch: 'amd64', homeDir: '/home', version: '1.0', commitHash: '', buildTime: '', updatedAt: Date.now() }}
      autoRegistered={opts?.autoRegistered ?? false}
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

  it('"deregister..." visible for manually-registered workers', () => {
    renderMenu({ autoRegistered: false })
    expect(screen.getByText('Deregister...')).toBeInTheDocument()
  })

  it('"deregister..." hidden for the auto-registered local worker', () => {
    // Tearing down the bundled worker would just trigger a re-register
    // on next launch — the menu item would be a dead-end click.
    renderMenu({ autoRegistered: true })
    expect(screen.queryByText('Deregister...')).not.toBeInTheDocument()
  })
})
