import type { Component } from 'solid-js'
import type { TunnelInfo } from '~/api/tunnelApi'
import type { Worker } from '~/generated/leapmux/v1/worker_pb'
import type { WorkerInfo } from '~/lib/workerInfoCache'
import type { ChannelStatus } from '~/stores/workerChannelStatus.store'
import { For, Show } from 'solid-js'
import * as listStyles from '~/components/workspace/workspaceList.css'
import { TunnelContextMenu } from './TunnelContextMenu'
import { WorkerContextMenu } from './WorkerContextMenu'
import * as styles from './workerSection.css'

export interface WorkerSectionContentProps {
  workers: Worker[]
  workerInfo: (id: string) => WorkerInfo | null
  channelStatus: (id: string) => ChannelStatus
  tunnelsForWorker: (workerId: string) => TunnelInfo[]
  currentUserId: string
  onAddTunnel: (worker: Worker) => void
  onDeleteTunnel: (tunnelId: string) => void
  onDeregister: (worker: Worker) => void
}

const statusClass: Record<ChannelStatus, string> = {
  connected: styles.statusConnected,
  disconnected: styles.statusDisconnected,
}

export const WorkerSectionContent: Component<WorkerSectionContentProps> = (props) => {
  return (
    <div class={listStyles.sectionItems}>
      <Show
        when={props.workers.length > 0}
        fallback={<div class={listStyles.emptySection}>No workers</div>}
      >
        <For each={props.workers}>
          {worker => (
            <>
              <div class={listStyles.item}>
                <div
                  class={`${styles.statusDot} ${statusClass[props.channelStatus(worker.id)]}`}
                  data-status={props.channelStatus(worker.id)}
                />
                <span class={listStyles.itemTitle}>
                  {props.workerInfo(worker.id)?.name ?? '\u2014'}
                </span>
                <div class={listStyles.itemActions}>
                  <WorkerContextMenu
                    workerInfo={props.workerInfo(worker.id)}
                    isOwner={worker.registeredBy === props.currentUserId}
                    onAddTunnel={() => props.onAddTunnel(worker)}
                    onDeregister={() => props.onDeregister(worker)}
                  />
                </div>
              </div>
              <For each={props.tunnelsForWorker(worker.id)}>
                {tunnel => (
                  <div class={`${listStyles.item} ${styles.tunnelItem}`}>
                    <span class={listStyles.itemTitle}>
                      {tunnel.type === 'socks5'
                        ? `SOCKS5 ${tunnel.bindAddr}:${tunnel.bindPort}`
                        : `${tunnel.bindAddr}:${tunnel.bindPort} \u2192 ${tunnel.targetAddr}:${tunnel.targetPort}`}
                    </span>
                    <div class={listStyles.itemActions}>
                      <TunnelContextMenu onDelete={() => props.onDeleteTunnel(tunnel.id)} />
                    </div>
                  </div>
                )}
              </For>
            </>
          )}
        </For>
      </Show>
    </div>
  )
}
