/// <reference types="vitest/globals" />
import type { TunnelInfo } from '~/api/platformBridge'
import type { Worker } from '~/generated/leapmux/v1/worker_pb'
import type { WorkerInfo } from '~/lib/workerInfoCache'
import type { ChannelStatus } from '~/stores/workerChannelStatus.store'
import { create } from '@bufbuild/protobuf'
import { render, screen } from '@solidjs/testing-library'
import { describe, expect, it, vi } from 'vitest'
import { actionSlot, actionSlotResting } from '~/components/tree/sidebarActions.css'
import * as listStyles from '~/components/workspace/workspaceList.css'
import { TunnelProvider } from '~/context/TunnelContext'
import { WorkerSchema } from '~/generated/leapmux/v1/worker_pb'
import { createTunnelStore } from '~/stores/tunnel.store'
import { clippedText } from '~/styles/shared.css'
import { hoverForTooltip, stubClipped, stubFitting } from '~/test-support/clipStub'
import { classSelector } from '~/test-support/composedClass'
import { WorkerSectionContent } from './WorkerSectionContent'

/** A port-forward tunnel, whose label is the one that carries the arrow. */
function portForward(): TunnelInfo {
  return { id: 't1', workerId: 'w1', type: 'port_forward', bindAddr: '127.0.0.1', bindPort: 3000, targetAddr: '10.0.0.1', targetPort: 8080 }
}

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

  const { container } = render(() => (
    <TunnelProvider store={tunnelStore}>
      <WorkerSectionContent
        workers={workers}
        workerInfo={() => defaultWorkerInfo}
        channelStatus={() => 'connected' as ChannelStatus}
        onAddTunnel={onAddTunnel}
        onDeregister={onDeregister}
      />
    </TunnelProvider>
  ))

  return { onAddTunnel, onDeregister, container }
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

  // The status dot rests where the three-dot trigger appears, so the row's
  // right edge stays put when the trigger fades in on hover. The swap itself is
  // a stylesheet rule (jsdom loads none), so what is asserted here is the
  // structure it depends on: both live in ONE actionSlot cell, and the dot
  // carries the resting class the rule keys off.
  it('rests the status dot in the same slot as the context-menu trigger', () => {
    const { container } = renderSection()

    const slot = container.querySelector(`.${actionSlot}`)
    expect(slot).toBeTruthy()

    const dot = slot!.querySelector('[data-status]')
    expect(dot).toBeTruthy()
    expect(dot!.className).toContain(actionSlotResting)

    // Same cell, not side by side: a sibling pair is what keeps the row from
    // widening when the trigger appears.
    const trigger = slot!.querySelector('button[aria-expanded]')
    expect(trigger).toBeTruthy()
    expect(dot!.parentElement).toBe(trigger!.closest(`.${actionSlot}`))
  })

  // The dot shares a cell with the trigger, and an element at `opacity: 0` still
  // hit-tests -- so a faded dot in front of the trigger makes a visible
  // three-dot menu refuse to open. jsdom resolves no stylesheet, so this asserts
  // the class that carries `pointer-events: none` rather than the computed
  // value; the CSS rule lives with the class in sidebarActions.css.ts.
  it('keeps the status dot out of the trigger\'s hit area', () => {
    const { container } = renderSection()
    const dot = container.querySelector(`.${actionSlot} [data-status]`)!
    expect(dot.className).toContain(actionSlotResting)
  })

  /**
   * The list fills the section width so a row CLIPS its name.
   *
   * It used to render into `sectionItems`, which sizes to its widest row so the
   * workspace tree can scroll sideways to reveal a deep path. That width made
   * the ellipsis on a worker name unreachable and scrolled the sidebar instead.
   * jsdom loads no stylesheet, so the class is what a unit test can see.
   */
  it('renders into the workers list container, not the workspace one', () => {
    const { container } = renderSection()
    expect(container.firstElementChild!.className).toMatch(/workerItems/)
    expect(container.firstElementChild!.className).not.toMatch(/sectionItems/)
  })

  it('clips the worker name and keeps its test id on the label', () => {
    renderSection()
    const name = screen.getByTestId('worker-name')
    expect(name.textContent).toBe('test-worker')
    // Token membership, not a substring: a future class whose own name merely
    // CONTAINS "clippedText" would satisfy a regex and prove nothing.
    expect(name.className.trim().split(/\s+/)).toContain(clippedText)
    expect(name.className).toMatch(/itemTitle/)
  })

  it('clips a tunnel label the same way', () => {
    const { container } = renderSection({ tunnels: [portForward()] })
    const label = [...container.querySelectorAll(classSelector(listStyles.itemTitle))]
      .find(el => el.textContent?.includes('→'))!
    expect(label.className.trim().split(/\s+/)).toContain(clippedText)
  })

  // The class alone does not prove the reader can recover the name. These pin
  // the other half of the pairing: once the label is clipped, the full string
  // is on the tooltip, and while it fits there is no tooltip to read.
  describe('clipped label tooltip', () => {
    beforeEach(() => {
      vi.useFakeTimers()
    })

    afterEach(() => {
      vi.useRealTimers()
      vi.restoreAllMocks()
    })

    it('gives the full worker name on hover once the label is clipped', () => {
      renderSection()
      const name = screen.getByTestId('worker-name')
      stubClipped(name)
      expect(hoverForTooltip(name)?.textContent).toBe('test-worker')
    })

    it('shows no tooltip while the worker name fits', () => {
      renderSection()
      const name = screen.getByTestId('worker-name')
      stubFitting(name)
      expect(hoverForTooltip(name)).toBeNull()
    })

    it('gives the full tunnel label on hover once it is clipped', () => {
      const { container } = renderSection({ tunnels: [portForward()] })
      const label = [...container.querySelectorAll<HTMLElement>(classSelector(listStyles.itemTitle))]
        .find(el => el.textContent?.includes('→'))!
      stubClipped(label)
      expect(hoverForTooltip(label)?.textContent).toBe('127.0.0.1:3000 → 10.0.0.1:8080')
    })
  })

  // The dot carries the channel state as COLOUR, so it needs a text
  // alternative; `aria-label` needs a role to attach to.
  it('gives the status dot an accessible name, not colour alone', () => {
    const { container } = renderSection()
    const dot = container.querySelector('[data-status]')!
    expect(dot.getAttribute('role')).toBe('img')
    expect(dot.getAttribute('aria-label')).toBe('Connected')
  })

  it('shows dash when workerInfo is null', () => {
    const tunnelStore = createTunnelStore()
    render(() => (
      <TunnelProvider store={tunnelStore}>
        <WorkerSectionContent
          workers={[makeWorker('w1')]}
          workerInfo={() => null}
          channelStatus={() => 'connected' as ChannelStatus}
          onAddTunnel={vi.fn()}
          onDeregister={vi.fn()}
        />
      </TunnelProvider>
    ))
    expect(screen.getByText('\u2014')).toBeInTheDocument()
  })
})
