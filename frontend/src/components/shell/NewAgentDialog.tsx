import type { Component } from 'solid-js'
import type { AgentInfo } from '~/generated/leapmux/v1/agent_pb'
import LoaderCircle from 'lucide-solid/icons/loader-circle'
import RefreshCw from 'lucide-solid/icons/refresh-cw'
import { createEffect, createSignal, For, on, onMount, Show } from 'solid-js'
import { workerClient } from '~/api/clients'
import { agentLoadingTimeoutMs } from '~/api/transport'
import * as workerRpc from '~/api/workerRpc'
import { Dialog } from '~/components/common/Dialog'
import { Icon } from '~/components/common/Icon'
import { isAgentCreateDisabled } from '~/components/shell/dialogValidation'
import { WorktreeOptions } from '~/components/shell/WorktreeOptions'
import { DirectoryTree } from '~/components/tree/DirectoryTree'
import { useOrg } from '~/context/OrgContext'
import { createLoadingSignal } from '~/hooks/createLoadingSignal'
import { createWorkerInfoStore } from '~/stores/workerInfo.store'
import { spinner } from '~/styles/animations.css'
import { errorText, labelRow, refreshButton, spinning, treeContainer } from '~/styles/shared.css'

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
  const org = useOrg()
  const workerInfoStore = createWorkerInfoStore()
  const [workers, setWorkers] = createSignal<import('~/generated/leapmux/v1/worker_pb').Worker[]>([])
  const [workerId, setWorkerId] = createSignal('')
  const [workingDir, setWorkingDir] = createSignal(props.defaultWorkingDir ?? '')
  const submitting = createLoadingSignal(agentLoadingTimeoutMs(false))
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
      // Fetch system info for homeDir via E2EE.
      for (const w of online) {
        workerInfoStore.fetchWorkerInfo(w.id)
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
        const resp = await workerRpc.getGitInfo(wid, {
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
    if (!workerId() || !workingDir().trim())
      return

    submitting.start()
    setError(null)
    try {
      const resp = await workerRpc.openAgent(workerId(), {
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
      submitting.stop()
    }
  }

  return (
    <Dialog title="New Agent" tall onClose={() => props.onClose()}>
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
                  <Icon icon={RefreshCw} size="sm" class={refreshing() ? spinning : ''} />
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
                  {(b) => {
                    const info = () => workerInfoStore.workerInfo(b.id)
                    const label = () => {
                      const i = info()
                      if (!i)
                        return b.id
                      const details = [i.version, i.os, i.arch].filter(Boolean).join(', ')
                      return details ? `${i.name} (${details})` : i.name
                    }
                    return <option value={b.id}>{label()}</option>
                  }}
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
                    homeDir={workerInfoStore.getHomeDir(workerId())}
                  />
                </div>
              </Show>
            </label>
            <Show when={workerId()}>
              <WorktreeOptions
                workerId={workerId()}
                selectedPath={workingDir()}
                homeDir={workerInfoStore.getHomeDir(workerId())}
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
            disabled={isAgentCreateDisabled({ submitting: submitting.loading(), workerId: workerId(), workingDir: workingDir(), createWorktree: createWorktree(), worktreeBranchError: worktreeBranchError() })}
          >
            <Show when={submitting.loading()}><Icon icon={LoaderCircle} size="sm" class={spinner} /></Show>
            {submitting.loading() ? 'Creating...' : 'Create'}
          </button>
        </footer>
      </form>
    </Dialog>
  )
}
