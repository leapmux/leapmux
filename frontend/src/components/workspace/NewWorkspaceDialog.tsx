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
import { AgentProviderSelector } from '~/components/shell/AgentProviderSelector'
import { isWorkspaceCreateDisabled } from '~/components/shell/dialogValidation'
import { DirectorySelector } from '~/components/shell/DirectorySelector'
import { GitOptions } from '~/components/shell/GitOptions'
import { WorkerSelector } from '~/components/shell/WorkerSelector'
import { AgentProvider } from '~/generated/leapmux/v1/agent_pb'
import { TabType } from '~/generated/leapmux/v1/workspace_pb'
import { createWorkerDialogState } from '~/hooks/createWorkerDialogState'
import { sanitizeName } from '~/lib/validate'
import { spinner } from '~/styles/animations.css'
import { dialogLeftPanel, dialogRightPanel, dialogSingleColumn, dialogTopSection, dialogTwoColumn, dialogWide, errorText, labelRow } from '~/styles/shared.css'

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
  const [agentProvider, setAgentProvider] = createSignal<AgentProvider>(AgentProvider.CLAUDE_CODE)
  const titleError = createMemo(() => sanitizeName(title()).error)

  const handleSubmit = async (e: Event) => {
    e.preventDefault()
    if (!state.workerId() || !state.workingDir().trim())
      return

    setSubmitting(true)
    state.setError(null)
    let createdWorkspaceId: string | undefined
    try {
      const wsResp = await workspaceClient.createWorkspace({
        orgId: state.org.orgId(),
        title: title().trim(),
      })
      if (!wsResp.workspace)
        throw new Error('No workspace in response')
      createdWorkspaceId = wsResp.workspace.id

      const wid = state.workerId()
      const agentResp = await workerRpc.openAgent(wid, {
        workspaceId: wsResp.workspace.id,
        agentProvider: agentProvider(),
        model: '',
        title: 'Agent 1',
        systemPrompt: '',
        workerId: wid,
        workingDir: state.workingDir(),
        createWorktree: state.gitMode() === 'create-worktree',
        worktreeBranch: state.worktreeBranch(),
        worktreeBaseBranch: state.gitMode() === 'create-worktree' ? state.worktreeBaseBranch() : '',
        checkoutBranch: state.gitMode() === 'switch-branch' ? state.checkoutBranch() : '',
        createBranch: state.gitMode() === 'create-branch' ? state.createBranch() : '',
        createBranchBase: state.gitMode() === 'create-branch' ? state.createBranchBase() : '',
        useWorktreePath: state.gitMode() === 'use-worktree' ? state.useWorktreePath() : '',
      })

      if (agentResp.agent) {
        workspaceClient.addTab({
          workspaceId: wsResp.workspace.id,
          tab: { tabType: TabType.AGENT, tabId: agentResp.agent.id, workerId: wid },
        }).catch(() => {})
      }

      props.onCreated(wsResp.workspace, wid)
    }
    catch (err) {
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
    <Dialog title="New Workspace" tall class={dialogWide} onClose={() => props.onClose()}>
      <form onSubmit={handleSubmit}>
        <section>
          <div class="vstack gap-4">
            <div class={state.showGitOptions() ? dialogTopSection : undefined}>
              <WorkerSelector state={state} />
              <AgentProviderSelector value={agentProvider} onChange={setAgentProvider} />
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
            </div>
            <div class={state.showGitOptions() ? dialogTwoColumn : dialogSingleColumn}>
              <div class={dialogLeftPanel}>
                <DirectorySelector state={state} />
              </div>
              <div class={state.showGitOptions() ? dialogRightPanel : undefined}>
                <Show when={state.workerId()}>
                  <GitOptions
                    workerId={state.workerId()}
                    selectedPath={state.workingDir()}
                    homeDir={state.workerInfoStore.getHomeDir(state.workerId())}
                    refreshKey={state.refreshKey()}
                    onGitModeChange={state.handleGitModeChange}
                    onVisibilityChange={state.setShowGitOptions}
                  />
                </Show>
              </div>
            </div>
          </div>
          <Show when={state.error()}>
            <div class={errorText}>{state.error()}</div>
          </Show>
        </section>
        <footer>
          <button type="button" class="outline" onClick={() => props.onClose()}>
            Cancel
          </button>
          <button
            type="submit"
            disabled={isWorkspaceCreateDisabled({ submitting: submitting(), workerId: state.workerId(), workingDir: state.workingDir(), titleError: titleError(), gitMode: state.gitMode(), worktreeBranchError: state.worktreeBranchError(), checkoutBranch: state.checkoutBranch(), createBranchError: state.createBranchError(), useWorktreePath: state.useWorktreePath() })}
          >
            <Show when={submitting()}><Icon icon={LoaderCircle} size="sm" class={spinner} /></Show>
            {submitting() ? 'Creating...' : 'Create'}
          </button>
        </footer>
      </form>
    </Dialog>
  )
}
