import { render, screen } from '@solidjs/testing-library'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { LauncherView } from './LauncherView'

const bridgeMocks = vi.hoisted(() => ({
  getRuntimeState: vi.fn(),
  getStartupInfo: vi.fn(),
  checkFullDiskAccess: vi.fn(),
  restoreWindowGeometry: vi.fn(),
}))

vi.mock('~/api/platformBridge', async (importOriginal) => {
  const actual = await importOriginal<typeof import('~/api/platformBridge')>()
  return {
    ...actual,
    getRuntimeState: bridgeMocks.getRuntimeState,
    restoreWindowGeometry: bridgeMocks.restoreWindowGeometry,
    platformBridge: {
      ...actual.platformBridge,
      getStartupInfo: bridgeMocks.getStartupInfo,
      checkFullDiskAccess: bridgeMocks.checkFullDiskAccess,
    },
  }
})

describe('launcherView', () => {
  beforeEach(() => {
    window.history.replaceState({}, '', '/?cleanup_error=lease%20release%20failed')
    bridgeMocks.getRuntimeState.mockResolvedValue({ shellMode: 'launcher', connected: false })
    bridgeMocks.getStartupInfo.mockResolvedValue({
      config: { mode: '', hub_url: '', window_width: 0, window_height: 0, window_mode: 'normal' },
      buildInfo: { version: '', commitHash: '', commitTime: '', buildTime: '', branch: '' },
    })
    bridgeMocks.checkFullDiskAccess.mockResolvedValue(true)
    bridgeMocks.restoreWindowGeometry.mockResolvedValue(undefined)
  })

  it('surfaces a cleanup warning carried across committed navigation', async () => {
    render(() => <LauncherView onConnected={() => {}} />)
    expect(await screen.findByText('lease release failed')).toBeInTheDocument()
  })
})
