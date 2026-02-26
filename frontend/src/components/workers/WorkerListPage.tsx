import type { Component } from 'solid-js'
import type { WorkerSettingsTab } from './WorkerSettingsDialog'
import type { Worker } from '~/generated/leapmux/v1/worker_pb'
import { A, useParams } from '@solidjs/router'
import { createEffect, createSignal, For, onMount, Show } from 'solid-js'
import { workerClient } from '~/api/clients'
import { useOrg } from '~/context/OrgContext'
import { WorkerContextMenu } from './WorkerContextMenu'
import * as styles from './WorkerListPage.css'
import { WorkerSettingsDialog } from './WorkerSettingsDialog'

export const WorkerListPage: Component = () => {
  const org = useOrg()
  const params = useParams<{ orgSlug: string }>()

  const [workers, setWorkers] = createSignal<Worker[]>([])
  const [loading, setLoading] = createSignal(true)
  const [error, setError] = createSignal<string | null>(null)

  // Settings dialog state
  const [settingsTarget, setSettingsTarget] = createSignal<{ worker: Worker, tab: WorkerSettingsTab } | null>(null)

  const fetchWorkers = async () => {
    const orgId = org.orgId()
    if (!orgId)
      return

    setLoading(true)
    setError(null)
    try {
      const resp = await workerClient.listWorkers({ orgId })
      setWorkers(resp.workers)
    }
    catch (e) {
      setError(e instanceof Error ? e.message : 'Failed to load workers')
    }
    finally {
      setLoading(false)
    }
  }

  onMount(() => {
    fetchWorkers()
  })

  // Re-fetch when org changes
  createEffect(() => {
    void org.orgId()
    fetchWorkers()
  })

  const renderCard = (worker: Worker) => (
    <div class={`card hstack justify-between ${styles.workerCard}`} data-testid="worker-card">
      <div class={styles.cardInfo}>
        <div class={styles.cardName} data-testid="worker-name">{worker.name}</div>
        <div class={styles.cardHostname} data-testid="worker-hostname">{worker.hostname}</div>
        <div class={styles.cardMeta}>
          {worker.os}
          /
          {worker.arch}
        </div>
      </div>
      <div class={styles.cardRight}>
        <span
          class={worker.online ? 'badge success' : 'badge outline'}
          data-testid="worker-status"
        >
          {worker.online ? 'Online' : 'Offline'}
        </span>
        <WorkerContextMenu
          onRename={() => setSettingsTarget({ worker, tab: 'general' })}
          onDeregister={() => setSettingsTarget({ worker, tab: 'deregister' })}
        />
      </div>
    </div>
  )

  return (
    <div class={styles.container}>
      <A href={`/o/${params.orgSlug}`} class={styles.backLink}>&larr; Dashboard</A>
      <h1>Workers</h1>

      <Show when={error()}>
        <div class={styles.errorText}>{error()}</div>
      </Show>

      <Show when={loading()}>
        <div class={styles.emptyState}>Loading...</div>
      </Show>

      <Show when={!loading() && workers().length === 0 && !error()}>
        <div class={styles.emptyState}>No workers registered</div>
      </Show>

      <Show when={!loading() && workers().length > 0}>
        <div data-testid="worker-section-owned">
          <div class={styles.sectionHeader}>
            <span class={styles.sectionName}>My Workers</span>
            <span class={styles.sectionCount}>
              (
              {workers().length}
              )
            </span>
          </div>
          <div class={styles.cardGrid}>
            <For each={workers()}>
              {worker => renderCard(worker)}
            </For>
          </div>
        </div>
      </Show>

      {/* Worker settings dialog (rename, deregister) */}
      <Show when={settingsTarget()}>
        {target => (
          <WorkerSettingsDialog
            worker={target().worker}
            initialTab={target().tab}
            onClose={() => setSettingsTarget(null)}
            onRenamed={(newName) => {
              setWorkers(prev =>
                prev.map(b => b.id === target().worker.id ? { ...b, name: newName } : b),
              )
              setSettingsTarget(null)
            }}
            onDeregistered={() => {
              setWorkers(prev => prev.filter(b => b.id !== target().worker.id))
              setSettingsTarget(null)
            }}
          />
        )}
      </Show>
    </div>
  )
}
