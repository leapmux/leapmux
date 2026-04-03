import type { Component } from 'solid-js'
import type { Worker } from '~/generated/leapmux/v1/worker_pb'
import type { WorkerInfo } from '~/lib/workerInfoCache'
import type { ChannelStatus } from '~/stores/workerChannelStatus.store'
import ChevronRight from 'lucide-solid/icons/chevron-right'
import { createSignal, For, Show } from 'solid-js'
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
  const [expandedIds, setExpandedIds] = createSignal<Set<string>>(new Set())

  function isExpanded(id: string): boolean {
    return expandedIds().has(id)
  }

  function toggleExpanded(id: string) {
    setExpandedIds((prev) => {
      const next = new Set(prev)
      if (next.has(id))
        next.delete(id)
      else
        next.add(id)
      return next
    })
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
                <div
                  class={`${styles.statusDot} ${statusClass[props.channelStatus(worker.id)]}`}
                  data-status={props.channelStatus(worker.id)}
                />
                <span class={listStyles.itemTitle}>
                  {props.workerInfo(worker.id)?.name ?? '\u2014'}
                </span>
                <div class={sidebarActions}>
                  <WorkerContextMenu
                    workerInfo={props.workerInfo(worker.id)}
                    isOwner={worker.registeredBy === props.currentUserId}
                    onAddTunnel={() => props.onAddTunnel(worker)}
                    onDeregister={() => props.onDeregister(worker)}
                  />
                </div>
              </div>
              <Show when={tunnel}>
                <div class={`${shared.childrenWrapper} ${isExpanded(worker.id) ? shared.childrenWrapperExpanded : ''}`}>
                  <div class={shared.childrenInner}>
                    <For each={tunnel!.tunnelsForWorker(worker.id)}>
                      {t => (
                        <div class={`${listStyles.item} ${styles.tunnelItem}`}>
                          <span class={listStyles.itemTitle}>
                            {t.type === 'socks5'
                              ? `SOCKS5 ${t.bindAddr}:${t.bindPort}`
                              : `${t.bindAddr}:${t.bindPort} \u2192 ${t.targetAddr}:${t.targetPort}`}
                          </span>
                          <div class={sidebarActions}>
                            <TunnelContextMenu onDelete={() => { tunnel!.remove(t.id).catch(() => {}) }} />
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
    </div>
  )
}
