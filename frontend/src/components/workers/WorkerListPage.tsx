import type { Component } from 'solid-js'
import type { WorkerSettingsTab } from './WorkerSettingsDialog'
import type { Worker } from '~/generated/leapmux/v1/worker_pb'
import { A, useParams } from '@solidjs/router'
import { createEffect, createMemo, createSignal, For, onMount, Show } from 'solid-js'
import { workerClient } from '~/api/clients'
import { useAuth } from '~/context/AuthContext'
import { useOrg } from '~/context/OrgContext'
import { ShareMode } from '~/generated/leapmux/v1/common_pb'
import { WorkerContextMenu } from './WorkerContextMenu'
import * as styles from './WorkerListPage.css'
import { WorkerSettingsDialog } from './WorkerSettingsDialog'

export const WorkerListPage: Component = () => {
  const auth = useAuth()
  const org = useOrg()
  const params = useParams<{ orgSlug: string }>()

  const [workers, setWorkers] = createSignal<Worker[]>([])
  const [loading, setLoading] = createSignal(true)
  const [error, setError] = createSignal<string | null>(null)

  // Settings dialog state
  const [settingsTarget, setSettingsTarget] = createSignal<{ worker: Worker, tab: WorkerSettingsTab } | null>(null)

  const currentUserId = () => auth.user()?.id

  const ownedWorkers = createMemo(() =>
    workers().filter(b => b.registeredBy === currentUserId()),
  )

  const sharedWorkers = createMemo(() =>
    workers().filter(b => b.registeredBy !== currentUserId()),
  )

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

  const shareModeLabel = (mode: ShareMode) => {
    switch (mode) {
      case ShareMode.ORG: return 'Org'
      case ShareMode.MEMBERS: return 'Members'
      default: return 'Private'
    }
  }

  const renderCard = (worker: Worker, owned: boolean) => (
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
        <Show when={owned}>
          <span class="badge secondary" data-testid="worker-share-mode">
            {shareModeLabel(worker.shareMode)}
          </span>
          <WorkerContextMenu
            onRename={() => setSettingsTarget({ worker, tab: 'general' })}
            onSharing={() => setSettingsTarget({ worker, tab: 'sharing' })}
            onDeregister={() => setSettingsTarget({ worker, tab: 'deregister' })}
          />
        </Show>
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
        {/* My Workers section */}
        <div data-testid="worker-section-owned">
          <div class={styles.sectionHeader}>
            <span class={styles.sectionName}>My Workers</span>
            <span class={styles.sectionCount}>
              (
              {ownedWorkers().length}
              )
            </span>
          </div>
          <Show
            when={ownedWorkers().length > 0}
            fallback={<div class={styles.emptySection}>No workers registered</div>}
          >
            <div class={styles.cardGrid}>
              <For each={ownedWorkers()}>
                {worker => renderCard(worker, true)}
              </For>
            </div>
          </Show>
        </div>

        {/* Shared with me section — only show when non-empty */}
        <Show when={sharedWorkers().length > 0}>
          <div data-testid="worker-section-shared">
            <div class={styles.sectionHeader}>
              <span class={styles.sectionName}>Shared with me</span>
              <span class={styles.sectionCount}>
                (
                {sharedWorkers().length}
                )
              </span>
            </div>
            <div class={styles.cardGrid}>
              <For each={sharedWorkers()}>
                {worker => renderCard(worker, false)}
              </For>
            </div>
          </div>
        </Show>
      </Show>

      {/* Worker settings dialog (rename, sharing, deregister) */}
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
            onShareModeChanged={(newShareMode: ShareMode) => {
              setWorkers(prev =>
                prev.map(b => b.id === target().worker.id ? { ...b, shareMode: newShareMode } : b),
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
