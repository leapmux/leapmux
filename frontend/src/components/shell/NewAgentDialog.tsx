import type { Component } from 'solid-js'
import type { AgentInfo, AgentProvider } from '~/generated/leapmux/v1/agent_pb'
import LoaderCircle from 'lucide-solid/icons/loader-circle'
import { createEffect, createMemo, createSignal, Show } from 'solid-js'
import { agentLoadingTimeoutMs } from '~/api/transport'
import * as workerRpc from '~/api/workerRpc'
import { Dialog, DialogColumns, DialogTopRow, DialogTopSection } from '~/components/common/Dialog'
import { labelRow } from '~/components/common/Dialog.css'
import { Icon } from '~/components/common/Icon'
import { AgentProviderSelector } from '~/components/shell/AgentProviderSelector'
import { isAgentCreateDisabled } from '~/components/shell/dialogValidation'
import { DirectorySelector } from '~/components/shell/DirectorySelector'
import { GitOptions } from '~/components/shell/GitOptions'
import { WorkerSelector } from '~/components/shell/WorkerSelector'
import { createLoadingSignal } from '~/hooks/createLoadingSignal'
import { createWorkerDialogState } from '~/hooks/createWorkerDialogState'
import { useMruProviders } from '~/hooks/useMruProviders'
import { getAvailableAgentProviders } from '~/lib/agentProviders'
import { validateSessionId } from '~/lib/validate'
import { spinner } from '~/styles/animations.css'
import { errorText } from '~/styles/shared.css'

interface NewAgentDialogProps {
  workspaceId: string
  defaultWorkerId?: string
  defaultWorkingDir?: string
  defaultModel?: string
  defaultTitle?: string
  defaultAgentProvider?: AgentProvider
  availableProviders?: AgentProvider[]
  onRefreshProviders?: () => void
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

  const available = () => getAvailableAgentProviders(props.availableProviders)
  const { mruProviders, recordProviderUse } = useMruProviders(available, 1)
  const [agentProvider, setAgentProvider] = createSignal(mruProviders()[0])
  const noProviders = () => available().length === 0

  createEffect(() => {
    const best = mruProviders()[0]
    if (best !== undefined && !available().includes(agentProvider()))
      setAgentProvider(best)
  })

  const [sessionId, setSessionId] = createSignal('')
  const sessionIdError = createMemo(() => {
    const v = sessionId().trim()
    if (!v)
      return null
    return validateSessionId(v)
  })

  const handleSubmit = async (e: Event) => {
    e.preventDefault()
    if (!props.workspaceId || !state.workerId() || !state.workingDir().trim())
      return

    submitting.start()
    state.setError(null)
    try {
      const resp = await workerRpc.openAgent(state.workerId(), {
        workspaceId: props.workspaceId,
        agentProvider: agentProvider(),
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
        ...(sessionId().trim() ? { agentSessionId: sessionId().trim() } : {}),
      })
      if (resp.agent) {
        recordProviderUse(agentProvider())
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
    <Dialog title="New Agent" tall wide busy={submitting.loading()} onClose={() => props.onClose()}>
      <form onSubmit={handleSubmit}>
        <section>
          <div class="vstack gap-4">
            <DialogTopSection>
              <DialogTopRow>
                <WorkerSelector state={state} />
                <AgentProviderSelector
                  value={agentProvider}
                  onChange={setAgentProvider}
                  availableProviders={props.availableProviders}
                  onRefresh={props.onRefreshProviders}
                />
              </DialogTopRow>
            </DialogTopSection>
            <DialogColumns
              left={<DirectorySelector state={state} />}
              right={(
                <>
                  <div>
                    <div class={labelRow}>Resume an existing session</div>
                    <input
                      type="text"
                      value={sessionId()}
                      onInput={e => setSessionId(e.currentTarget.value)}
                      placeholder="Session ID"
                    />
                    <Show when={sessionIdError()}>
                      <span class={errorText}>{sessionIdError()}</span>
                    </Show>
                  </div>
                  <Show when={state.workerId() && !state.worktreeResolving()}>
                    <GitOptions
                      workerId={state.workerId()}
                      selectedPath={state.workingDir()}
                      homeDir={state.workerInfoStore.getHomeDir(state.workerId())}
                      refreshKey={state.refreshKey()}
                      onGitModeChange={state.handleGitModeChange}
                      onVisibilityChange={state.setShowGitOptions}
                    />
                  </Show>
                </>
              )}
            />
          </div>
          <Show when={state.error()}>
            <div class={errorText}>{state.error()}</div>
          </Show>
        </section>
        <footer>
          <button type="button" class="outline" disabled={submitting.loading()} onClick={() => props.onClose()}>
            Cancel
          </button>
          <button
            type="submit"
            disabled={isAgentCreateDisabled({ submitting: submitting.loading(), workspaceId: props.workspaceId, workerId: state.workerId(), workingDir: state.workingDir(), noProviders: noProviders(), sessionIdError: sessionIdError(), gitMode: state.gitMode(), worktreeBranchError: state.worktreeBranchError(), checkoutBranch: state.checkoutBranch(), createBranchError: state.createBranchError(), useWorktreePath: state.useWorktreePath() })}
          >
            <Show when={submitting.loading()}><Icon icon={LoaderCircle} size="sm" class={spinner} /></Show>
            {submitting.loading() ? 'Creating...' : 'Create'}
          </button>
        </footer>
      </form>
    </Dialog>
  )
}
