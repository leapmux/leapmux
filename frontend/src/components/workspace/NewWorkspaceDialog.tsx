import type { Component } from 'solid-js'
import type { AgentProvider } from '~/generated/leapmux/v1/agent_pb'
import type { Tab } from '~/stores/tab.types'
import type { WorkspaceStoreRegistryType } from '~/stores/workspaceStoreRegistry'
import { generateSlug } from 'random-word-slugs'
import { createMemo, createSignal, Show } from 'solid-js'
import { channelClient, workspaceClient } from '~/api/clients'
import * as workerRpc from '~/api/workerRpc'
import { openAgentRequestOptions } from '~/components/chat/providers/registry'
import { DialogColumns, DialogTopRow, DialogTopSection } from '~/components/common/Dialog'
import { labelRow } from '~/components/common/Dialog.css'
import { RefreshButton } from '~/components/common/RefreshButton'
import { AgentProviderSelector } from '~/components/shell/AgentProviderSelector'
import { isWorkspaceCreateDisabled } from '~/components/shell/dialogValidation'
import { DirectorySelector } from '~/components/shell/DirectorySelector'
import { GitOptions } from '~/components/shell/GitOptions'
import { GitOptionsLoader } from '~/components/shell/GitOptionsLoader'
import { SessionIdInput } from '~/components/shell/SessionIdInput'
import { DialogFormFooter, WorkerDialogShell } from '~/components/shell/WorkerDialogShell'
import { WorkerSelector } from '~/components/shell/WorkerSelector'
import { useOrg } from '~/context/OrgContext'
import { TabType } from '~/generated/leapmux/v1/workspace_pb'
import { createDirectoryTreeState } from '~/hooks/createDirectoryTreeState'
import { createSessionIdState } from '~/hooks/createSessionIdState'
import { useAgentProviderSelection } from '~/hooks/useAgentProviderSelection'
import { useWorkerDialog } from '~/hooks/useWorkerDialog'
import { seedTabIntoNewWorkspace } from '~/lib/crdt'
import { sanitizeName } from '~/lib/validate'
import { protoToAgentTabFields, tabKey } from '~/stores/tab.helpers'
import { errorText } from '~/styles/shared.css'

interface NewWorkspaceDialogProps {
  onCreated: (workspaceId: string) => void
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
  const org = useOrg()
  const { submit: { submitting, error, formHandler }, worker, gitMode, pathInfo } = useWorkerDialog({
    submit: { fallback: 'Failed to create workspace' },
    // eslint-disable-next-line solid/reactivity -- one-time initial value
    worker: { preselectedWorkerId: props.preselectedWorkerId },
  })
  const tree = createDirectoryTreeState()
  const randomTitle = () => generateSlug(3, { format: 'title' })
  const [title, setTitle] = createSignal(randomTitle())

  const { agentProvider, setAgentProvider, recordProviderUse, noProviders } = useAgentProviderSelection(
    () => props.availableProviders,
  )
  const titleError = createMemo(() => sanitizeName(title()).error)

  const sessionId = createSessionIdState()

  const submitDisabled = () => isWorkspaceCreateDisabled({
    submitting: submitting.loading(),
    workerId: worker.workerId(),
    workingDir: worker.workingDir(),
    noProviders: noProviders(),
    titleError: titleError(),
    sessionIdError: sessionId.error(),
    git: gitMode.currentIntent(),
  })

  const handleSubmit = formHandler(submitDisabled, async () => {
    let createdWorkspaceId: string | undefined
    try {
      const wsResp = await workspaceClient.createWorkspace({
        orgId: org.orgId(),
        title: title().trim(),
      })
      if (!wsResp.workspaceId)
        throw new Error('No workspace ID in response')
      createdWorkspaceId = wsResp.workspaceId

      const wid = worker.workerId()
      const provider = agentProvider()
      // submitDisabled gates on noProviders(); reaching here with
      // provider===undefined would mean the submit slipped past the
      // guard, so fail loudly before the proto serializer turns it into
      // enum 0.
      if (provider === undefined)
        throw new Error('No agent provider available')
      await channelClient.prepareWorkspaceAccess({ workerId: wid, workspaceId: wsResp.workspaceId })
      const agentResp = await workerRpc.openAgent(wid, {
        workspaceId: wsResp.workspaceId,
        agentProvider: provider,
        // title omitted: worker picks "Agent <Name>" from the shared pool.
        workerId: wid,
        workingDir: worker.workingDir(),
        ...openAgentRequestOptions(provider),
        ...gitMode.toGitFields(),
        ...(sessionId.trimmed() ? { agentSessionId: sessionId.trimmed() } : {}),
      })

      if (agentResp.agent) {
        recordProviderUse(provider)
        // After the worker has spawned the agent, wait for the
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

      props.onCreated(wsResp.workspaceId)
    }
    catch (err) {
      // Roll back the speculative workspace on partial failure before
      // useDialogSubmit captures the error — without this, a failed
      // agent spawn would leave an empty workspace orphaned in the
      // backend.
      if (createdWorkspaceId) {
        workspaceClient.deleteWorkspace({ workspaceId: createdWorkspaceId }).catch(() => {})
      }
      throw err
    }
  })

  return (
    <WorkerDialogShell
      title="New workspace"
      submitting={submitting.loading()}
      error={error()}
      onSubmit={handleSubmit}
      onClose={() => props.onClose()}
      footer={(
        <DialogFormFooter
          submitting={submitting.loading()}
          submitDisabled={submitDisabled()}
          submitLabel="Create"
          submittingLabel="Creating..."
          onClose={() => props.onClose()}
        />
      )}
    >
      <DialogTopSection>
        <DialogTopRow>
          <WorkerSelector state={worker} />
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
        left={<DirectorySelector state={worker} tree={tree} />}
        right={(
          <>
            <SessionIdInput state={sessionId} />
            <Show when={worker.workerId()}>
              <GitOptionsLoader gitInfo={pathInfo}>
                {() => (
                  <GitOptions
                    workerId={worker.workerId()}
                    selectedPath={worker.workingDir()}
                    homeDir={worker.getHomeDir()}
                    gitInfo={pathInfo}
                    gitMode={gitMode.gitMode}
                    refreshKey={tree.treeKey()}
                    onGitModeChange={gitMode.handleGitModeChange}
                  />
                )}
              </GitOptionsLoader>
            </Show>
          </>
        )}
      />
    </WorkerDialogShell>
  )
}
