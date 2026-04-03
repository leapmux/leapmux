import type { Component } from 'solid-js'
import type { TunnelInfo } from '~/api/tunnelApi'
import type { Worker } from '~/generated/leapmux/v1/worker_pb'
import type { WorkerInfo } from '~/lib/workerInfoCache'
import type { ChannelStatus } from '~/stores/workerChannelStatus.store'
import ArrowBigRightDash from 'lucide-solid/icons/arrow-big-right-dash'
import ChevronRight from 'lucide-solid/icons/chevron-right'
import ChevronsLeftRightEllipsis from 'lucide-solid/icons/chevrons-left-right-ellipsis'
import { createSignal, For, Show } from 'solid-js'
import { ConfirmDialog } from '~/components/common/ConfirmDialog'
import * as shared from '~/components/tree/sharedTree.css'
import { sidebarActions } from '~/components/tree/sidebarActions.css'
import * as listStyles from '~/components/workspace/workspaceList.css'
import { useTunnel } from '~/context/TunnelContext'
import { TunnelContextMenu } from './TunnelContextMenu'
import { WorkerContextMenu } from './WorkerContextMenu'
import * as styles from './workerSection.css'

export interface WorkerSectionContentProps {
  workers: Worker[]
  workerInfo: (id: string) => WorkerInfo | null
  channelStatus: (id: string) => ChannelStatus
  currentUserId: string
  onAddTunnel: (worker: Worker) => void
  onDeregister: (worker: Worker) => void
}

const statusClass: Record<ChannelStatus, string> = {
  connected: styles.statusConnected,
  disconnected: styles.statusDisconnected,
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
    <div class={listStyles.sectionItems}>
      <Show
        when={props.workers.length > 0}
        fallback={<div class={listStyles.emptySection}>No workers</div>}
      >
        <For each={props.workers}>
          {worker => (
            <>
              <div
                class={listStyles.item}
                onClick={() => toggleExpanded(worker.id)}
              >
                <ChevronRight
                  size={14}
                  class={`${shared.chevron} ${isExpanded(worker.id) ? shared.chevronExpanded : ''}`}
                />
                <span class={listStyles.itemTitle}>
                  {props.workerInfo(worker.id)?.name ?? '\u2014'}
                </span>
                <div
                  class={`${styles.statusDot} ${statusClass[props.channelStatus(worker.id)]}`}
                  data-status={props.channelStatus(worker.id)}
                />
                <div class={sidebarActions}>
                  <WorkerContextMenu
                    workerInfo={props.workerInfo(worker.id)}
                    isOwner={worker.registeredBy === props.currentUserId}
                    hasTunnels={!!tunnel && tunnel.tunnelsForWorker(worker.id).length > 0}
                    onAddTunnel={() => props.onAddTunnel(worker)}
                    onDeleteAllTunnels={() => setDeleteAllTunnelsWorkerId(worker.id)}
                    onDeregister={() => props.onDeregister(worker)}
                  />
                </div>
              </div>
              <Show when={tunnel}>
                <div class={`${shared.childrenWrapper} ${isExpanded(worker.id) ? shared.childrenWrapperExpanded : ''}`}>
                  <div class={shared.childrenInner}>
                    <For each={tunnel!.tunnelsForWorker(worker.id)}>
                      {t => (
                        <div class={`${shared.node} ${styles.tunnelItem}`}>
                          {t.type === 'socks5'
                            ? <ChevronsLeftRightEllipsis size={14} class={styles.tunnelIcon} />
                            : <ArrowBigRightDash size={14} class={styles.tunnelIcon} />}
                          <span class={listStyles.itemTitle}>
                            {tunnelLabel(t)}
                          </span>
                          <div class={sidebarActions}>
                            <TunnelContextMenu onDelete={() => setDeleteTunnelTarget(t)} />
                          </div>
                        </div>
                      )}
                    </For>
                  </div>
                </div>
              </Show>
            </>
          )}
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
