import type { Component } from 'solid-js'
import LoaderCircle from 'lucide-solid/icons/loader-circle'
import RefreshCw from 'lucide-solid/icons/refresh-cw'
import { createMemo, createSignal, For, onMount, Show } from 'solid-js'
import { workerClient } from '~/api/clients'
import { useOrg } from '~/context/OrgContext'
import { spinner } from '~/styles/animations.css'
import { dialogCompact, errorText, labelRow, refreshButton, spinning } from '~/styles/shared.css'

interface ResumeSessionDialogProps {
  defaultWorkerId?: string
  onResume: (sessionId: string, workerId: string) => void
  onClose: () => void
}

const SESSION_ID_PATTERN = /^[\w-]+$/

export const ResumeSessionDialog: Component<ResumeSessionDialogProps> = (props) => {
  let dialogRef!: HTMLDialogElement
  const org = useOrg()
  const [workers, setWorkers] = createSignal<import('~/generated/leapmux/v1/worker_pb').Worker[]>([])
  const [workerId, setWorkerId] = createSignal('')
  const [sessionId, setSessionId] = createSignal('')
  const [submitting, setSubmitting] = createSignal(false)
  const [refreshing, setRefreshing] = createSignal(false)

  const validationError = createMemo(() => {
    const value = sessionId().trim()
    if (!value)
      return null // Don't show error for empty input
    if (!SESSION_ID_PATTERN.test(value))
      return 'Only letters, numbers, dashes, and underscores are allowed.'
    return null
  })

  const canSubmit = createMemo(() => {
    const value = sessionId().trim()
    return value.length > 0 && workerId().length > 0 && !validationError() && !submitting()
  })

  const fetchWorkers = async () => {
    try {
      const resp = await workerClient.listWorkers({ orgId: org.orgId() })
      const online = resp.workers.filter(b => b.online)
      setWorkers(online)
      if (online.length > 0 && !workerId()) {
        setWorkerId(online[0].id)
      }
      return online.length > 0
    }
    catch {
      return false
    }
  }

  onMount(async () => {
    dialogRef.showModal()
    await fetchWorkers()
    // Pre-select worker if specified and online
    if (props.defaultWorkerId) {
      const match = workers().find(b => b.id === props.defaultWorkerId)
      if (match) {
        setWorkerId(match.id)
      }
    }
  })

  const handleRefresh = async () => {
    setRefreshing(true)
    await fetchWorkers()
    setRefreshing(false)
  }

  const handleSubmit = (e: Event) => {
    e.preventDefault()
    if (!canSubmit())
      return

    setSubmitting(true)
    props.onResume(sessionId().trim(), workerId())
  }

  return (
    <dialog ref={dialogRef} class={dialogCompact} onClose={() => props.onClose()}>
      <header><h2>Resume an existing session</h2></header>
      <form onSubmit={handleSubmit}>
        <section>
          <div class="vstack gap-4">
            <label>
              <div class={labelRow}>
                Worker
                <button
                  type="button"
                  class={refreshButton}
                  onClick={handleRefresh}
                  disabled={refreshing()}
                  title="Refresh workers"
                >
                  <RefreshCw size={14} class={refreshing() ? spinning : ''} />
                </button>
              </div>
              <select
                value={workerId()}
                onChange={e => setWorkerId(e.currentTarget.value)}
              >
                <Show when={workers().length === 0}>
                  <option value="">No workers online</option>
                </Show>
                <For each={workers()}>
                  {b => <option value={b.id}>{b.name}</option>}
                </For>
              </select>
            </label>
            <label>
              Session ID
              <input
                type="text"
                value={sessionId()}
                onInput={e => setSessionId(e.currentTarget.value)}
                placeholder="e.g. abc123-def456"
                autofocus
                data-testid="resume-session-id-input"
              />
            </label>
            <Show when={validationError()}>
              <span class={errorText}>{validationError()}</span>
            </Show>
          </div>
        </section>
        <footer>
          <button
            type="button"
            class="outline"
            onClick={() => props.onClose()}
          >
            Cancel
          </button>
          <button
            type="submit"
            disabled={!canSubmit()}
            data-testid="resume-session-submit"
          >
            <Show when={submitting()}>
              <LoaderCircle size={14} class={spinner} />
            </Show>
            Resume
          </button>
        </footer>
      </form>
    </dialog>
  )
}
