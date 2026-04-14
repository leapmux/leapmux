/// <reference types="vitest/globals" />
import type { TunnelInfo } from '~/api/platformBridge'
import type { Worker } from '~/generated/leapmux/v1/worker_pb'
import type { WorkerInfo } from '~/lib/workerInfoCache'
import type { ChannelStatus } from '~/stores/workerChannelStatus.store'
import { create } from '@bufbuild/protobuf'
import { render, screen } from '@solidjs/testing-library'
import { describe, expect, it, vi } from 'vitest'
import { TunnelProvider } from '~/context/TunnelContext'
import { WorkerSchema } from '~/generated/leapmux/v1/worker_pb'
import { createTunnelStore } from '~/stores/tunnel.store'
import { WorkerSectionContent } from './WorkerSectionContent'

vi.mock('~/api/platformBridge', async (importOriginal) => {
  const actual = await importOriginal<typeof import('~/api/platformBridge')>()
  return {
    ...actual,
    isTunnelAvailable: vi.fn(() => false),
    platformBridge: {
      ...actual.platformBridge,
      createTunnel: vi.fn(),
      deleteTunnel: vi.fn(),
      listTunnels: vi.fn(),
    },
  }
})

function makeWorker(id: string, registeredBy = 'user-1'): Worker {
  return create(WorkerSchema, { id, registeredBy, online: true })
}

const defaultWorkerInfo: WorkerInfo = {
  name: 'test-worker',
  os: 'linux',
  arch: 'amd64',
  homeDir: '/home/test',
  version: '1.0.0',
  commitHash: '',
  buildTime: '',
  updatedAt: Date.now(),
}

function renderSection(opts?: {
  workers?: Worker[]
  tunnels?: TunnelInfo[]
  currentUserId?: string
}) {
  const workers = opts?.workers ?? [makeWorker('w1')]
  const onAddTunnel = vi.fn()
  const onDeregister = vi.fn()

  // Create a tunnel store and pre-populate with test tunnels via the signal directly.
  const tunnelStore = createTunnelStore()
  if (opts?.tunnels?.length) {
    // Access internal signal to set test data without calling the API.
    const tunnels = opts.tunnels
    Object.defineProperty(tunnelStore, 'tunnels', {
      value: () => tunnels,
    })
    Object.defineProperty(tunnelStore, 'tunnelsForWorker', {
      value: (workerId: string) => tunnels.filter(t => t.workerId === workerId),
    })
  }

  render(() => (
    <TunnelProvider store={tunnelStore}>
      <WorkerSectionContent
        workers={workers}
        workerInfo={() => defaultWorkerInfo}
        channelStatus={() => 'connected' as ChannelStatus}
        currentUserId={opts?.currentUserId ?? 'user-1'}
        onAddTunnel={onAddTunnel}
        onDeregister={onDeregister}
      />
    </TunnelProvider>
  ))

  return { onAddTunnel, onDeregister }
}

describe('workerSectionContent', () => {
  it('no tunnels shown when list empty', () => {
    renderSection()
    expect(screen.queryByText(/\u2192/)).not.toBeInTheDocument()
    expect(screen.queryByText(/SOCKS5/)).not.toBeInTheDocument()
  })

  it('tunnels shown under correct worker', () => {
    const workers = [makeWorker('w1'), makeWorker('w2')]
    const tunnels: TunnelInfo[] = [
      { id: 't1', workerId: 'w1', type: 'port_forward', bindAddr: '127.0.0.1', bindPort: 3000, targetAddr: '10.0.0.1', targetPort: 8080 },
      { id: 't2', workerId: 'w2', type: 'socks5', bindAddr: '127.0.0.1', bindPort: 1080, targetAddr: '', targetPort: 0 },
    ]
    renderSection({ workers, tunnels })
    expect(screen.getByText(/127\.0\.0\.1:3000 \u2192 10\.0\.0\.1:8080/)).toBeInTheDocument()
    expect(screen.getByText(/SOCKS5 127\.0\.0\.1:1080/)).toBeInTheDocument()
  })

  it('port forward tunnel shows target info', () => {
    const tunnels: TunnelInfo[] = [
      { id: 't1', workerId: 'w1', type: 'port_forward', bindAddr: '127.0.0.1', bindPort: 3000, targetAddr: '10.0.0.1', targetPort: 8080 },
    ]
    renderSection({ tunnels })
    expect(screen.getByText(/127\.0\.0\.1:3000 \u2192 10\.0\.0\.1:8080/)).toBeInTheDocument()
  })

  it('socks5 tunnel shows bind info only', () => {
    const tunnels: TunnelInfo[] = [
      { id: 't1', workerId: 'w1', type: 'socks5', bindAddr: '127.0.0.1', bindPort: 1080, targetAddr: '', targetPort: 0 },
    ]
    renderSection({ tunnels })
    expect(screen.getByText(/SOCKS5 127\.0\.0\.1:1080/)).toBeInTheDocument()
    const tunnelText = screen.getByText(/SOCKS5 127\.0\.0\.1:1080/).textContent
    expect(tunnelText).not.toContain('\u2192')
  })

  it('renders worker name from workerInfo', () => {
    renderSection()
    expect(screen.getAllByText('test-worker').length).toBeGreaterThanOrEqual(1)
  })

  it('shows dash when workerInfo is null', () => {
    const tunnelStore = createTunnelStore()
    render(() => (
      <TunnelProvider store={tunnelStore}>
        <WorkerSectionContent
          workers={[makeWorker('w1')]}
          workerInfo={() => null}
          channelStatus={() => 'connected' as ChannelStatus}
          currentUserId="user-1"
          onAddTunnel={vi.fn()}
          onDeregister={vi.fn()}
        />
      </TunnelProvider>
    ))
    expect(screen.getByText('\u2014')).toBeInTheDocument()
  })
})
