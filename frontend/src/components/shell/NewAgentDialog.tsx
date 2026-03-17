import type { Component } from 'solid-js'
import type { AgentInfo } from '~/generated/leapmux/v1/agent_pb'
import LoaderCircle from 'lucide-solid/icons/loader-circle'
import { Show } from 'solid-js'
import { agentLoadingTimeoutMs } from '~/api/transport'
import * as workerRpc from '~/api/workerRpc'
import { Dialog } from '~/components/common/Dialog'
import { Icon } from '~/components/common/Icon'
import { isAgentCreateDisabled } from '~/components/shell/dialogValidation'
import { DirectorySelector } from '~/components/shell/DirectorySelector'
import { GitOptions } from '~/components/shell/GitOptions'
import { WorkerSelector } from '~/components/shell/WorkerSelector'
import { AgentProvider } from '~/generated/leapmux/v1/agent_pb'
import { createLoadingSignal } from '~/hooks/createLoadingSignal'
import { createWorkerDialogState } from '~/hooks/createWorkerDialogState'
import { spinner } from '~/styles/animations.css'
import { dialogLeftPanel, dialogRightPanel, dialogSingleColumn, dialogTopSection, dialogTwoColumn, dialogWide, errorText } from '~/styles/shared.css'

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
  const state = createWorkerDialogState({
    preselectedWorkerId: props.defaultWorkerId,
    defaultWorkingDir: props.defaultWorkingDir,
    resolveWorktree: true,
  })
  const submitting = createLoadingSignal(agentLoadingTimeoutMs(false))

  const handleSubmit = async (e: Event) => {
    e.preventDefault()
    if (!state.workerId() || !state.workingDir().trim())
      return

    submitting.start()
    state.setError(null)
    try {
      const resp = await workerRpc.openAgent(state.workerId(), {
        workspaceId: props.workspaceId,
        agentProvider: AgentProvider.CLAUDE_CODE,
        model: props.defaultModel ?? '',
        title: props.defaultTitle ?? '',
        systemPrompt: '',
        workerId: state.workerId(),
        workingDir: state.workingDir(),
        createWorktree: state.gitMode() === 'create-worktree',
        worktreeBranch: state.worktreeBranch(),
        worktreeBaseBranch: state.gitMode() === 'create-worktree' ? state.worktreeBaseBranch() : '',
        checkoutBranch: state.gitMode() === 'switch-branch' ? state.checkoutBranch() : '',
        createBranch: state.gitMode() === 'create-branch' ? state.createBranch() : '',
        createBranchBase: state.gitMode() === 'create-branch' ? state.createBranchBase() : '',
        useWorktreePath: state.gitMode() === 'use-worktree' ? state.useWorktreePath() : '',
        ...(props.sessionId ? { agentSessionId: props.sessionId } : {}),
      })
      if (resp.agent) {
        props.onCreated(resp.agent)
      }
    }
    catch (err) {
      state.setError(err instanceof Error ? err.message : 'Failed to create agent')
    }
    finally {
      submitting.stop()
    }
  }

  return (
    <Dialog title="New Agent" tall class={dialogWide} onClose={() => props.onClose()}>
      <form onSubmit={handleSubmit}>
        <section>
          <div class="vstack gap-4">
            <div class={state.showGitOptions() ? dialogTopSection : undefined}>
              <WorkerSelector state={state} />
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
            disabled={isAgentCreateDisabled({ submitting: submitting.loading(), workerId: state.workerId(), workingDir: state.workingDir(), gitMode: state.gitMode(), worktreeBranchError: state.worktreeBranchError(), checkoutBranch: state.checkoutBranch(), createBranchError: state.createBranchError(), useWorktreePath: state.useWorktreePath() })}
          >
            <Show when={submitting.loading()}><Icon icon={LoaderCircle} size="sm" class={spinner} /></Show>
            {submitting.loading() ? 'Creating...' : 'Create'}
          </button>
        </footer>
      </form>
    </Dialog>
  )
}
