import type { Component } from 'solid-js'
import type { AgentProvider } from '~/generated/leapmux/v1/agent_pb'
import LoaderCircle from 'lucide-solid/icons/loader-circle'
import { generateSlug } from 'random-word-slugs'
import { createEffect, createMemo, createSignal, Show } from 'solid-js'
import { channelClient, workspaceClient } from '~/api/clients'
import { apiLoadingTimeoutMs } from '~/api/transport'
import * as workerRpc from '~/api/workerRpc'
import { Dialog, DialogColumns, DialogTopRow, DialogTopSection } from '~/components/common/Dialog'
import { labelRow } from '~/components/common/Dialog.css'
import { Icon } from '~/components/common/Icon'
import { RefreshButton } from '~/components/common/RefreshButton'
import { AgentProviderSelector } from '~/components/shell/AgentProviderSelector'
import { isWorkspaceCreateDisabled } from '~/components/shell/dialogValidation'
import { DirectorySelector } from '~/components/shell/DirectorySelector'
import { GitOptions } from '~/components/shell/GitOptions'
import { WorkerSelector } from '~/components/shell/WorkerSelector'
import { TabType } from '~/generated/leapmux/v1/workspace_pb'
import { createLoadingSignal } from '~/hooks/createLoadingSignal'
import { createWorkerDialogState } from '~/hooks/createWorkerDialogState'
import { useMruProviders } from '~/hooks/useMruProviders'
import { getAvailableAgentProviders } from '~/lib/agentProviders'
import { sanitizeName, validateSessionId } from '~/lib/validate'
import { spinner } from '~/styles/animations.css'
import { errorText } from '~/styles/shared.css'

interface NewWorkspaceDialogProps {
  onCreated: (workspaceId: string, workerId: string) => void
  onClose: () => void
  preselectedWorkerId?: string
  availableProviders?: AgentProvider[]
  onRefreshProviders?: () => void
}

export const NewWorkspaceDialog: Component<NewWorkspaceDialogProps> = (props) => {
  // eslint-disable-next-line solid/reactivity -- one-time initial value
  const state = createWorkerDialogState({ preselectedWorkerId: props.preselectedWorkerId })
  const randomTitle = () => generateSlug(3, { format: 'title' })
  const [title, setTitle] = createSignal(randomTitle())
  const submitting = createLoadingSignal(apiLoadingTimeoutMs())

  const available = () => getAvailableAgentProviders(props.availableProviders)
  const { mruProviders, recordProviderUse } = useMruProviders(available, 1)
  const [agentProvider, setAgentProvider] = createSignal(mruProviders()[0])
  const noProviders = () => available().length === 0

  createEffect(() => {
    const best = mruProviders()[0]
    if (best !== undefined && !available().includes(agentProvider()))
      setAgentProvider(best)
  })
  const titleError = createMemo(() => sanitizeName(title()).error)

  const [sessionId, setSessionId] = createSignal('')
  const sessionIdError = createMemo(() => {
    const v = sessionId().trim()
    if (!v)
      return null
    return validateSessionId(v)
  })

  const handleSubmit = async (e: Event) => {
    e.preventDefault()
    if (!state.workerId() || !state.workingDir().trim())
      return

    submitting.start()
    state.setError(null)
    let createdWorkspaceId: string | undefined
    try {
      const wsResp = await workspaceClient.createWorkspace({
        orgId: state.org.orgId(),
        title: title().trim(),
      })
      if (!wsResp.workspaceId)
        throw new Error('No workspace ID in response')
      createdWorkspaceId = wsResp.workspaceId

      const wid = state.workerId()
      await channelClient.prepareWorkspaceAccess({ workerId: wid, workspaceId: wsResp.workspaceId })
      const agentResp = await workerRpc.openAgent(wid, {
        workspaceId: wsResp.workspaceId,
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
        ...(sessionId().trim() ? { agentSessionId: sessionId().trim() } : {}),
      })

      if (agentResp.agent) {
        recordProviderUse(agentProvider())
        workspaceClient.addTab({
          workspaceId: wsResp.workspaceId,
          tab: { tabType: TabType.AGENT, tabId: agentResp.agent.id, workerId: wid },
        }).catch(() => {})
      }

      props.onCreated(wsResp.workspaceId, wid)
    }
    catch (err) {
      if (createdWorkspaceId) {
        workspaceClient.deleteWorkspace({ workspaceId: createdWorkspaceId }).catch(() => {})
      }
      state.setError(err instanceof Error ? err.message : 'Failed to create workspace')
    }
    finally {
      submitting.stop()
    }
  }

  return (
    <Dialog title="New Workspace" tall wide busy={submitting.loading()} onClose={() => props.onClose()}>
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
            disabled={isWorkspaceCreateDisabled({ submitting: submitting.loading(), workerId: state.workerId(), workingDir: state.workingDir(), noProviders: noProviders(), titleError: titleError(), sessionIdError: sessionIdError(), gitMode: state.gitMode(), worktreeBranchError: state.worktreeBranchError(), checkoutBranch: state.checkoutBranch(), createBranchError: state.createBranchError(), useWorktreePath: state.useWorktreePath() })}
          >
            <Show when={submitting.loading()}><Icon icon={LoaderCircle} size="sm" class={spinner} /></Show>
            {submitting.loading() ? 'Creating...' : 'Create'}
          </button>
        </footer>
      </form>
    </Dialog>
  )
}
