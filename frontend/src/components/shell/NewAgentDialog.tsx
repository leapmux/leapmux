import type { Component } from 'solid-js'
import type { AgentInfo } from '~/generated/leapmux/v1/agent_pb'
import LoaderCircle from 'lucide-solid/icons/loader-circle'
import RefreshCw from 'lucide-solid/icons/refresh-cw'
import { createEffect, createSignal, For, on, onMount, Show } from 'solid-js'
import { agentClient, gitClient, workerClient } from '~/api/clients'
import { WorktreeOptions } from '~/components/shell/WorktreeOptions'
import { DirectoryTree } from '~/components/tree/DirectoryTree'
import { useOrg } from '~/context/OrgContext'
import { spinner } from '~/styles/animations.css'
import { dialogCompact, errorText, labelRow, refreshButton, spinning, treeContainer } from '~/styles/shared.css'

interface NewAgentDialogProps {
  workspaceId: string
  defaultWorkerId?: string
  defaultWorkingDir?: string
  defaultModel?: string
  defaultTitle?: string
  sessionId?: string
  onCreated: (agent: AgentInfo) => void
  onClose: () => void
}

export const NewAgentDialog: Component<NewAgentDialogProps> = (props) => {
  let dialogRef!: HTMLDialogElement
  const org = useOrg()
  const [workers, setWorkers] = createSignal<import('~/generated/leapmux/v1/worker_pb').Worker[]>([])
  const [workerId, setWorkerId] = createSignal('')
  const [workingDir, setWorkingDir] = createSignal(props.defaultWorkingDir ?? '~')
  const [submitting, setSubmitting] = createSignal(false)
  const [error, setError] = createSignal<string | null>(null)
  const [refreshing, setRefreshing] = createSignal(false)
  const [createWorktree, setCreateWorktree] = createSignal(false)
  const [worktreeBranch, setWorktreeBranch] = createSignal('')
  const [worktreeBranchError, setWorktreeBranchError] = createSignal<string | null>(null)

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
    if (props.defaultWorkerId) {
      const match = workers().find(b => b.id === props.defaultWorkerId)
      if (match) {
        setWorkerId(match.id)
      }
    }
  })

  // If the default working directory is a worktree, resolve to the original repo root.
  {
    let resolved = false
    createEffect(on(() => workerId(), async (wid) => {
      if (resolved || !wid || !props.defaultWorkingDir)
        return
      resolved = true
      try {
        const resp = await gitClient.getGitInfo({
          workerId: wid,
          path: props.defaultWorkingDir,
          orgId: org.orgId(),
        })
        if (resp.isWorktreeRoot && resp.repoRoot)
          setWorkingDir(resp.repoRoot)
      }
      catch {}
    }))
  }

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
      const resp = await agentClient.openAgent({
        workspaceId: props.workspaceId,
        model: props.defaultModel ?? '',
        title: props.defaultTitle ?? '',
        systemPrompt: '',
        workerId: workerId(),
        workingDir: workingDir(),
        createWorktree: createWorktree(),
        worktreeBranch: worktreeBranch(),
        ...(props.sessionId ? { agentSessionId: props.sessionId } : {}),
      })
      if (resp.agent) {
        props.onCreated(resp.agent)
      }
    }
    catch (err) {
      setError(err instanceof Error ? err.message : 'Failed to create agent')
    }
    finally {
      setSubmitting(false)
    }
  }

  return (
    <dialog ref={dialogRef} class={dialogCompact} onClose={() => props.onClose()}>
      <header><h2>New Agent</h2></header>
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
            disabled={submitting() || !workerId() || (createWorktree() && !!worktreeBranchError())}
          >
            <Show when={submitting()}><LoaderCircle size={14} class={spinner} /></Show>
            {submitting() ? 'Creating...' : 'Create'}
          </button>
        </footer>
      </form>
    </dialog>
  )
}
