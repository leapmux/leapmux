import type { Component } from 'solid-js'
import type { Worker } from '~/generated/leapmux/v1/worker_pb'
import type { WorkerInfo } from '~/lib/workerInfoCache'
import type { ChannelStatus } from '~/stores/workerChannelStatus.store'
import { For, Show } from 'solid-js'
import * as listStyles from '~/components/workspace/workspaceList.css'
import { WorkerContextMenu } from './WorkerContextMenu'
import * as styles from './workerSection.css'

export interface WorkerSectionContentProps {
  workers: Worker[]
  workerInfo: (id: string) => WorkerInfo | null
  channelStatus: (id: string) => ChannelStatus
  onDeregister: (worker: Worker) => void
}

const statusClass: Record<ChannelStatus, string> = {
  connected: styles.statusConnected,
  warning: styles.statusWarning,
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
            <div class={listStyles.item}>
              <div
                class={`${styles.statusDot} ${statusClass[props.channelStatus(worker.id)]}`}
              />
              <span class={listStyles.itemTitle}>
                {props.workerInfo(worker.id)?.name ?? '\u2014'}
              </span>
              <WorkerContextMenu
                workerInfo={props.workerInfo(worker.id)}
                onDeregister={() => props.onDeregister(worker)}
              />
            </div>
          )}
        </For>
      </Show>
    </div>
  )
}
