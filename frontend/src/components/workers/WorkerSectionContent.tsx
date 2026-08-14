import type { Component } from 'solid-js'
import type { TunnelInfo } from '~/api/platformBridge'
import type { Worker } from '~/generated/leapmux/v1/worker_pb'
import type { WorkerInfo } from '~/lib/workerInfoCache'
import type { ChannelStatus } from '~/stores/workerChannelStatus.store'
import ArrowBigRightDash from 'lucide-solid/icons/arrow-big-right-dash'
import ChevronRight from 'lucide-solid/icons/chevron-right'
import ChevronsLeftRightEllipsis from 'lucide-solid/icons/chevrons-left-right-ellipsis'
import { createMemo, createSignal, For, Show } from 'solid-js'
import { ClippedText } from '~/components/common/ClippedText'
import { ConfirmDialog } from '~/components/common/ConfirmDialog'
import { createContextMenuAnchor } from '~/components/common/DropdownMenu'
import { StatusDot } from '~/components/common/StatusDot'
import * as shared from '~/components/tree/sharedTree.css'
import { actionSlot, actionSlotResting, sidebarActions } from '~/components/tree/sidebarActions.css'
import * as listStyles from '~/components/workspace/workspaceList.css'
import { useTunnel } from '~/context/TunnelContext'
import { TunnelContextMenu } from './TunnelContextMenu'
import { WorkerContextMenu } from './WorkerContextMenu'
import * as styles from './workerSection.css'

export interface WorkerSectionContentProps {
  workers: Worker[]
  workerInfo: (id: string) => WorkerInfo | null
  channelStatus: (id: string) => ChannelStatus
  onAddTunnel: (worker: Worker) => void
  onDeregister: (worker: Worker) => void
}

const statusClass: Record<ChannelStatus, string> = {
  connected: styles.statusConnected,
  disconnected: styles.statusDisconnected,
}

/** What the dot's colour means. The dot has no other way to say it. */
const statusLabel: Record<ChannelStatus, string> = {
  connected: 'Connected',
  disconnected: 'Disconnected',
}

export const WorkerSectionContent: Component<WorkerSectionContentProps> = (props) => {
  const tunnel = useTunnel()
  const [collapsedIds, setCollapsedIds] = createSignal<Set<string>>(new Set())
  const [deleteTunnelTarget, setDeleteTunnelTarget] = createSignal<TunnelInfo | null>(null)
  const [deleteAllTunnelsWorkerId, setDeleteAllTunnelsWorkerId] = createSignal<string | null>(null)

  function isExpanded(id: string): boolean {
    return !collapsedIds().has(id)
  }

  function toggleExpanded(id: string) {
    setCollapsedIds((prev) => {
      const next = new Set(prev)
      if (next.has(id))
        next.delete(id)
      else
        next.add(id)
      return next
    })
  }

  function tunnelLabel(t: TunnelInfo): string {
    return t.type === 'socks5'
      ? `SOCKS5 ${t.bindAddr}:${t.bindPort}`
      : `${t.bindAddr}:${t.bindPort} \u2192 ${t.targetAddr}:${t.targetPort}`
  }

  return (
    <div class={styles.workerItems}>
      <Show
        when={props.workers.length > 0}
        fallback={<div class={listStyles.emptySection}>No workers</div>}
      >
        <For each={props.workers}>
          {(worker) => {
            const workerTunnels = () => tunnel?.tunnelsForWorker(worker.id) ?? []
            const workerName = createMemo(() => props.workerInfo(worker.id)?.name ?? '\u2014')
            const status = () => props.channelStatus(worker.id)
            // The row element, for its right-click / long-press menu.
            const [rowEl, setRowEl] = createContextMenuAnchor()
            return (
              <>
                <div
                  ref={setRowEl}
                  class={listStyles.item}
                  data-testid="worker-row"
                  onClick={() => toggleExpanded(worker.id)}
                >
                  <ChevronRight
                    size={14}
                    class={`${shared.chevron} ${isExpanded(worker.id) ? shared.chevronExpanded : ''}`}
                  />
                  <ClippedText
                    text={workerName()}
                    class={listStyles.itemTitle}
                    testId="worker-name"
                  />
                  <div class={sidebarActions}>
                    {/* The dot and the three-dot trigger share one cell: the
                        dot rests at the row's right edge and the trigger takes
                        its place on hover, so the row's width never shifts. */}
                    <div class={actionSlot}>
                      {/* No tooltip: `actionSlotResting` is `pointer-events:
                          none`, so a hover on this dot never reaches it. The
                          accessible name carries the state instead. */}
                      <StatusDot
                        class={`${actionSlotResting} ${statusClass[status()]}`}
                        label={statusLabel[status()]}
                        status={status()}
                      />
                      <WorkerContextMenu
                        contextMenuFor={rowEl}
                        workerInfo={props.workerInfo(worker.id)}
                        autoRegistered={worker.autoRegistered}
                        hasTunnels={workerTunnels().length > 0}
                        onAddTunnel={() => props.onAddTunnel(worker)}
                        onDeleteAllTunnels={() => setDeleteAllTunnelsWorkerId(worker.id)}
                        onDeregister={() => props.onDeregister(worker)}
                      />
                    </div>
                  </div>
                </div>
                <Show when={tunnel}>
                  <div class={`${shared.childrenWrapper} ${isExpanded(worker.id) ? shared.childrenWrapperExpanded : ''}`}>
                    <div class={shared.childrenInner}>
                      <For each={workerTunnels()}>
                        {(t) => {
                          const label = tunnelLabel(t)
                          const [tunnelRowEl, setTunnelRowEl] = createContextMenuAnchor()
                          return (
                            <div ref={setTunnelRowEl} class={`${shared.node} ${styles.tunnelItem}`}>
                              {t.type === 'socks5'
                                ? <ChevronsLeftRightEllipsis size={14} class={styles.tunnelIcon} />
                                : <ArrowBigRightDash size={14} class={styles.tunnelIcon} />}
                              <ClippedText text={label} class={listStyles.itemTitle} />
                              <div class={sidebarActions}>
                                <TunnelContextMenu
                                  contextMenuFor={tunnelRowEl}
                                  onDelete={() => setDeleteTunnelTarget(t)}
                                />
                              </div>
                            </div>
                          )
                        }}
                      </For>
                    </div>
                  </div>
                </Show>
              </>
            )
          }}
        </For>
      </Show>

      <Show when={deleteTunnelTarget()}>
        {target => (
          <ConfirmDialog
            title="Delete tunnel"
            danger
            confirmLabel="Delete"
            onConfirm={() => {
              tunnel!.remove(target().id).catch(() => {})
              setDeleteTunnelTarget(null)
            }}
            onCancel={() => setDeleteTunnelTarget(null)}
          >
            <p>
              {'Delete tunnel '}
              <strong>{tunnelLabel(target())}</strong>
              ?
            </p>
          </ConfirmDialog>
        )}
      </Show>

      <Show when={deleteAllTunnelsWorkerId()}>
        {workerId => (
          <ConfirmDialog
            title="Delete all tunnels"
            danger
            confirmLabel="Delete all"
            onConfirm={() => {
              tunnel!.removeAllForWorker(workerId()).catch(() => {})
              setDeleteAllTunnelsWorkerId(null)
            }}
            onCancel={() => setDeleteAllTunnelsWorkerId(null)}
          >
            <p>
              {'Delete all tunnels for worker '}
              <strong>{props.workerInfo(workerId())?.name ?? workerId()}</strong>
              ?
            </p>
          </ConfirmDialog>
        )}
      </Show>
    </div>
  )
}
