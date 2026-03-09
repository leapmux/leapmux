import type { Component } from 'solid-js'
import type { Workspace } from '~/generated/leapmux/v1/workspace_pb'
import LoaderCircle from 'lucide-solid/icons/loader-circle'
import RefreshCw from 'lucide-solid/icons/refresh-cw'
import { generateSlug } from 'random-word-slugs'
import { createMemo, createSignal, For, onMount, Show } from 'solid-js'
import { workerClient, workspaceClient } from '~/api/clients'
import * as workerRpc from '~/api/workerRpc'
import { Dialog } from '~/components/common/Dialog'
import { Icon } from '~/components/common/Icon'
import { WorktreeOptions } from '~/components/shell/WorktreeOptions'
import { DirectoryTree } from '~/components/tree/DirectoryTree'
import { useOrg } from '~/context/OrgContext'
import { TabType } from '~/generated/leapmux/v1/workspace_pb'
import { sanitizeName } from '~/lib/validate'
import { createWorkerInfoStore } from '~/stores/workerInfo.store'
import { spinner } from '~/styles/animations.css'
import { errorText, labelRow, refreshButton, spinning, treeContainer } from '~/styles/shared.css'

interface NewWorkspaceDialogProps {
  onCreated: (workspace: Workspace, workerId: string) => void
  onClose: () => void
  preselectedWorkerId?: string
}

export const NewWorkspaceDialog: Component<NewWorkspaceDialogProps> = (props) => {
  const org = useOrg()
  const workerInfoStore = createWorkerInfoStore()
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
  const titleError = createMemo(() => sanitizeName(title()).error)

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
      // 1. Create workspace on hub.
      const wsResp = await workspaceClient.createWorkspace({
        orgId: org.orgId(),
        title: title().trim(),
      })
      if (!wsResp.workspace)
        throw new Error('No workspace in response')

      // 2. Open the first agent on the selected worker.
      const wid = workerId()
      const agentResp = await workerRpc.openAgent(wid, {
        workspaceId: wsResp.workspace.id,
        model: '',
        title: 'Agent 1',
        systemPrompt: '',
        workerId: wid,
        workingDir: workingDir(),
        createWorktree: createWorktree(),
        worktreeBranch: worktreeBranch(),
      })

      // 3. Register the agent tab on the hub.
      if (agentResp.agent) {
        workspaceClient.addTab({
          workspaceId: wsResp.workspace.id,
          tab: { tabType: TabType.AGENT, tabId: agentResp.agent.id, workerId: wid },
        }).catch(() => {})
      }

      props.onCreated(wsResp.workspace, wid)
    }
    catch (err) {
      setError(err instanceof Error ? err.message : 'Failed to create workspace')
    }
    finally {
      setSubmitting(false)
    }
  }

  return (
    <Dialog title="New Workspace" tall onClose={() => props.onClose()}>
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
              <div class={labelRow}>
                Title
                <button
                  type="button"
                  class={refreshButton}
                  onClick={() => setTitle(randomTitle())}
                  title="Generate random name"
                >
                  <Icon icon={RefreshCw} size="sm" />
                </button>
              </div>
              <input
                type="text"
                value={title()}
                onInput={e => setTitle(sanitizeName(e.currentTarget.value).value)}
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
            disabled={submitting() || !workerId() || !!titleError() || (createWorktree() && !!worktreeBranchError())}
          >
            <Show when={submitting()}><Icon icon={LoaderCircle} size="sm" class={spinner} /></Show>
            {submitting() ? 'Creating...' : 'Create'}
          </button>
        </footer>
      </form>
    </Dialog>
  )
}
