/// <reference types="vitest/globals" />
import type { TunnelInfo } from '~/api/tunnelApi'
import type { Worker } from '~/generated/leapmux/v1/worker_pb'
import type { WorkerInfo } from '~/lib/workerInfoCache'
import type { ChannelStatus } from '~/stores/workerChannelStatus.store'
import { create } from '@bufbuild/protobuf'
import { render, screen } from '@solidjs/testing-library'
import { describe, expect, it, vi } from 'vitest'
import { WorkerSchema } from '~/generated/leapmux/v1/worker_pb'
import { WorkerSectionContent } from './WorkerSectionContent'

function makeWorker(id: string, registeredBy = 'user-1'): Worker {
  return create(WorkerSchema, { id, registeredBy, orgId: 'org-1', online: true })
}

const defaultWorkerInfo: WorkerInfo = {
  name: 'test-worker',
  os: 'linux',
  arch: 'amd64',
  homeDir: '/home/test',
  version: '1.0.0',
  updatedAt: Date.now(),
}

function renderSection(opts?: {
  workers?: Worker[]
  tunnels?: TunnelInfo[]
  currentUserId?: string
}) {
  const workers = opts?.workers ?? [makeWorker('w1')]
  const tunnels = opts?.tunnels ?? []
  const onAddTunnel = vi.fn()
  const onDeleteTunnel = vi.fn()
  const onDeregister = vi.fn()

  render(() => (
    <WorkerSectionContent
      workers={workers}
      workerInfo={() => defaultWorkerInfo}
      channelStatus={() => 'connected' as ChannelStatus}
      tunnelsForWorker={(workerId: string) => tunnels.filter(t => t.workerId === workerId)}
      currentUserId={opts?.currentUserId ?? 'user-1'}
      onAddTunnel={onAddTunnel}
      onDeleteTunnel={onDeleteTunnel}
      onDeregister={onDeregister}
    />
  ))

  return { onAddTunnel, onDeleteTunnel, onDeregister }
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

    // Worker w1 should show port forward tunnel.
    expect(screen.getByText(/127\.0\.0\.1:3000 \u2192 10\.0\.0\.1:8080/)).toBeInTheDocument()
    // Worker w2 should show SOCKS5 tunnel.
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
    // Should NOT show arrow for SOCKS5.
    const tunnelText = screen.getByText(/SOCKS5 127\.0\.0\.1:1080/).textContent
    expect(tunnelText).not.toContain('\u2192')
  })

  it('renders worker name from workerInfo', () => {
    renderSection()
    expect(screen.getByText('test-worker')).toBeInTheDocument()
  })

  it('shows dash when workerInfo is null', () => {
    render(() => (
      <WorkerSectionContent
        workers={[makeWorker('w1')]}
        workerInfo={() => null}
        channelStatus={() => 'connected' as ChannelStatus}
        tunnelsForWorker={() => []}
        currentUserId="user-1"
        onAddTunnel={vi.fn()}
        onDeleteTunnel={vi.fn()}
        onDeregister={vi.fn()}
      />
    ))
    expect(screen.getByText('\u2014')).toBeInTheDocument()
  })
})
