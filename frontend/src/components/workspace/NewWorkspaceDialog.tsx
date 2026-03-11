import type { Component } from 'solid-js'
import type { Workspace } from '~/generated/leapmux/v1/workspace_pb'
import LoaderCircle from 'lucide-solid/icons/loader-circle'
import { generateSlug } from 'random-word-slugs'
import { createMemo, createSignal, Show } from 'solid-js'
import { workspaceClient } from '~/api/clients'
import * as workerRpc from '~/api/workerRpc'
import { Dialog } from '~/components/common/Dialog'
import { Icon } from '~/components/common/Icon'
import { RefreshButton } from '~/components/common/RefreshButton'
import { isWorkspaceCreateDisabled } from '~/components/shell/dialogValidation'
import { DirectorySelector } from '~/components/shell/DirectorySelector'
import { WorkerSelector } from '~/components/shell/WorkerSelector'
import { WorktreeOptions } from '~/components/shell/WorktreeOptions'
import { TabType } from '~/generated/leapmux/v1/workspace_pb'
import { createWorkerDialogState } from '~/hooks/createWorkerDialogState'
import { sanitizeName } from '~/lib/validate'
import { spinner } from '~/styles/animations.css'
import { errorText, labelRow } from '~/styles/shared.css'

interface NewWorkspaceDialogProps {
  onCreated: (workspace: Workspace, workerId: string) => void
  onClose: () => void
  preselectedWorkerId?: string
}

export const NewWorkspaceDialog: Component<NewWorkspaceDialogProps> = (props) => {
  // eslint-disable-next-line solid/reactivity -- one-time initial value
  const state = createWorkerDialogState({ preselectedWorkerId: props.preselectedWorkerId })
  const randomTitle = () => generateSlug(3, { format: 'title' })
  const [title, setTitle] = createSignal(randomTitle())
  const [submitting, setSubmitting] = createSignal(false)
  const titleError = createMemo(() => sanitizeName(title()).error)

  const handleSubmit = async (e: Event) => {
    e.preventDefault()
    if (!state.workerId() || !state.workingDir().trim())
      return

    setSubmitting(true)
    state.setError(null)
    let createdWorkspaceId: string | undefined
    try {
      // 1. Create workspace on hub.
      const wsResp = await workspaceClient.createWorkspace({
        orgId: state.org.orgId(),
        title: title().trim(),
      })
      if (!wsResp.workspace)
        throw new Error('No workspace in response')
      createdWorkspaceId = wsResp.workspace.id

      // 2. Open the first agent on the selected worker.
      const wid = state.workerId()
      const agentResp = await workerRpc.openAgent(wid, {
        workspaceId: wsResp.workspace.id,
        model: '',
        title: 'Agent 1',
        systemPrompt: '',
        workerId: wid,
        workingDir: state.workingDir(),
        createWorktree: state.createWorktree(),
        worktreeBranch: state.worktreeBranch(),
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
      // Roll back the workspace if it was created but a subsequent step failed.
      if (createdWorkspaceId) {
        workspaceClient.deleteWorkspace({ workspaceId: createdWorkspaceId }).catch(() => {})
      }
      state.setError(err instanceof Error ? err.message : 'Failed to create workspace')
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
            <WorkerSelector state={state} />
            <div>
              <div class={labelRow}>
                Title
                <RefreshButton onClick={() => setTitle(randomTitle())} title="Generate random name" />
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
            </div>
            <DirectorySelector state={state} />
            <Show when={state.workerId()}>
              <WorktreeOptions
                workerId={state.workerId()}
                selectedPath={state.workingDir()}
                homeDir={state.workerInfoStore.getHomeDir(state.workerId())}
                onWorktreeChange={state.handleWorktreeChange}
              />
            </Show>
            <Show when={state.error()}>
              <div class={errorText}>{state.error()}</div>
            </Show>
          </div>
        </section>
        <footer>
          <button type="button" class="outline" onClick={() => props.onClose()}>
            Cancel
          </button>
          <button
            type="submit"
            disabled={isWorkspaceCreateDisabled({ submitting: submitting(), workerId: state.workerId(), workingDir: state.workingDir(), titleError: titleError(), createWorktree: state.createWorktree(), worktreeBranchError: state.worktreeBranchError() })}
          >
            <Show when={submitting()}><Icon icon={LoaderCircle} size="sm" class={spinner} /></Show>
            {submitting() ? 'Creating...' : 'Create'}
          </button>
        </footer>
      </form>
    </Dialog>
  )
}
