import type { Component } from 'solid-js'
import type { AgentProvider } from '~/generated/leapmux/v1/agent_pb'
import type { Tab } from '~/stores/tab.types'
import type { WorkspaceStoreRegistryType } from '~/stores/workspaceStoreRegistry'
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
import { seedTabIntoNewWorkspace } from '~/lib/crdt'
import { sanitizeName, validateSessionId } from '~/lib/validate'
import { protoToAgentTabFields, tabKey } from '~/stores/tab.helpers'
import { spinner } from '~/styles/animations.css'
import { errorText } from '~/styles/shared.css'

interface NewWorkspaceDialogProps {
  onCreated: (workspaceId: string, workerId: string) => void
  onClose: () => void
  preselectedWorkerId?: string
  availableProviders?: AgentProvider[]
  onRefreshProviders?: () => void
  /**
   * Workspace store registry. Pre-seeded with the new workspace's
   * agent + tab snapshot BEFORE `onCreated` navigates, so
   * `useWorkspaceRestore` takes its `cached.restored` fast path and
   * renders the agent's tab immediately — instead of racing
   * `listTabs` against the SetTabRegister echo and then letting the
   * projection reconciler insert a bare tab (no title, no
   * agentProvider, agent metadata missing → "Agent not found").
   */
  registry: WorkspaceStoreRegistryType
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
        // title omitted: worker picks "Agent <Name>" from the shared pool.
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
        // Seed-tab batch (plan §"User-visible flow for CreateWorkspace"):
        // after the worker has spawned the agent, wait for the
        // `WorkspaceCreated` event to populate `WorkspaceContentsRecord
        // .root_node_id` in the speculative state, then submit
        // `SetTabRegister(tile_id=root_node_id) + position + worker_id`
        // for the new agent. Without this, the agent exists on the
        // worker but is invisible to all clients via the CRDT
        // projection — they'd render an empty workspace until the
        // user touched another tab.
        const seed = await seedTabIntoNewWorkspace({
          workspaceId: wsResp.workspaceId,
          tabType: TabType.AGENT,
          tabId: agentResp.agent.id,
          workerId: wid,
        })

        // Pre-seed the per-workspace registry snapshot so the
        // post-navigation `useWorkspaceRestore` takes its
        // `cached.restored` fast path. Without this, the navigation
        // races `listTabs` against the SetTabRegister echo — when
        // `listTabs` wins (the common case, since the seed batch is
        // still in the opsSubmitter's 16ms aggregator), the tabStore
        // is wiped and the CRDT-projection reconciler later re-inserts
        // the tab with only CRDT-driven fields (tile_id / position /
        // worker_id). The agent record from `agentResp.agent` is the
        // only place the title / agentProvider / git metadata lives on
        // this client; without pre-seeding, the new workspace renders
        // the tab as the raw agent id in the sidebar and "Agent not
        // found" in the tile.
        if (seed) {
          const newTab: Tab = {
            type: TabType.AGENT,
            id: agentResp.agent.id,
            tileId: seed.rootNodeId,
            position: seed.position,
            ...protoToAgentTabFields(agentResp.agent.workerId, agentResp.agent),
          }
          props.registry.set(wsResp.workspaceId, {
            workspaceId: wsResp.workspaceId,
            tabs: [newTab],
            activeTabKey: tabKey(newTab),
            tileActiveTabKeys: { [seed.rootNodeId]: tabKey(newTab) },
            // Layout state is bridge-driven (a memo over the CRDT
            // projection for the active workspaceId); the cached
            // value here only seeds focusedTileId so the next
            // openAgent click lands on the right LEAF.
            layout: { root: { type: 'leaf', id: seed.rootNodeId }, focusedTileId: seed.rootNodeId },
            restored: true,
            tabsLoaded: true,
          })
        }
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
    <Dialog title="New workspace" tall wide busy={submitting.loading()} onClose={() => props.onClose()}>
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
