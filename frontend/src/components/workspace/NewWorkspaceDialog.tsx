import type { Component } from 'solid-js'
import type { Workspace } from '~/generated/leapmux/v1/workspace_pb'
import LoaderCircle from 'lucide-solid/icons/loader-circle'
import RefreshCw from 'lucide-solid/icons/refresh-cw'
import { generateSlug } from 'random-word-slugs'
import { createMemo, createSignal, For, onMount, Show } from 'solid-js'
import { workerClient, workspaceClient } from '~/api/clients'
import { WorktreeOptions } from '~/components/shell/WorktreeOptions'
import { DirectoryTree } from '~/components/tree/DirectoryTree'
import { useOrg } from '~/context/OrgContext'
import { validateName } from '~/lib/validate'
import { spinner } from '~/styles/animations.css'
import { dialogCompact, errorText, labelRow, refreshButton, spinning, treeContainer } from '~/styles/shared.css'

interface NewWorkspaceDialogProps {
  onCreated: (workspace: Workspace) => void
  onClose: () => void
  preselectedWorkerId?: string
}

export const NewWorkspaceDialog: Component<NewWorkspaceDialogProps> = (props) => {
  let dialogRef!: HTMLDialogElement
  const org = useOrg()
  const [workers, setWorkers] = createSignal<import('~/generated/leapmux/v1/worker_pb').Worker[]>([])
  const [workerId, setWorkerId] = createSignal('')
  const randomTitle = () => generateSlug(3, { format: 'title' })
  const [title, setTitle] = createSignal(randomTitle())
  const [workingDir, setWorkingDir] = createSignal('~')
  const [submitting, setSubmitting] = createSignal(false)
  const [error, setError] = createSignal<string | null>(null)
  const [refreshing, setRefreshing] = createSignal(false)
  const [createWorktree, setCreateWorktree] = createSignal(false)
  const [worktreeBranch, setWorktreeBranch] = createSignal('')
  const [worktreeBranchError, setWorktreeBranchError] = createSignal<string | null>(null)
  const titleError = createMemo(() => validateName(title()))

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
    catch (e) {
      setError(e instanceof Error ? e.message : 'Failed to load workers')
      return false
    }
  }

  // Fetch on mount only
  onMount(async () => {
    dialogRef.showModal()
    await fetchWorkers()
    // Pre-select worker if specified and online
    if (props.preselectedWorkerId) {
      const match = workers().find(b => b.id === props.preselectedWorkerId)
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

  const handleSubmit = async (e: Event) => {
    e.preventDefault()
    if (!workerId())
      return

    setSubmitting(true)
    setError(null)
    try {
      const resp = await workspaceClient.createWorkspace({
        workerId: workerId(),
        orgId: org.orgId(),
        title: title().trim(),
        workingDir: workingDir(),
        createWorktree: createWorktree(),
        worktreeBranch: worktreeBranch(),
      })
      if (resp.workspace) {
        props.onCreated(resp.workspace)
      }
    }
    catch (err) {
      setError(err instanceof Error ? err.message : 'Failed to create workspace')
    }
    finally {
      setSubmitting(false)
    }
  }

  return (
    <dialog ref={dialogRef} class={dialogCompact} onClose={() => props.onClose()}>
      <header><h2>New Workspace</h2></header>
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
              <div class={labelRow}>
                Title
                <button
                  type="button"
                  class={refreshButton}
                  onClick={() => setTitle(randomTitle())}
                  title="Generate random name"
                >
                  <RefreshCw size={14} />
                </button>
              </div>
              <input
                type="text"
                value={title()}
                onInput={e => setTitle(e.currentTarget.value)}
                placeholder="New Workspace"
              />
              <Show when={titleError()}>
                <div class={errorText}>{titleError()}</div>
              </Show>
            </label>
            <label>
              Working Directory
              <Show when={workerId()}>
                <div class={treeContainer}>
                  <DirectoryTree
                    workerId={workerId()}
                    selectedPath={workingDir()}
                    onSelect={setWorkingDir}
                    rootPath="~"
                  />
                </div>
              </Show>
            </label>
            <Show when={workerId()}>
              <WorktreeOptions
                workerId={workerId()}
                selectedPath={workingDir()}
                onWorktreeChange={(create, branch, branchError) => {
                  setCreateWorktree(create)
                  setWorktreeBranch(branch)
                  setWorktreeBranchError(branchError)
                }}
              />
            </Show>
            <Show when={error()}>
              <div class={errorText}>{error()}</div>
            </Show>
          </div>
        </section>
        <footer>
          <button type="button" class="outline" onClick={() => props.onClose()}>
            Cancel
          </button>
          <button
            type="submit"
            disabled={submitting() || !workerId() || !!titleError() || (createWorktree() && !!worktreeBranchError())}
          >
            <Show when={submitting()}><LoaderCircle size={14} class={spinner} /></Show>
            {submitting() ? 'Creating...' : 'Create'}
          </button>
        </footer>
      </form>
    </dialog>
  )
}
