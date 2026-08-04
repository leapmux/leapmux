import type { Component } from 'solid-js'
import type { AgentProvider } from '~/generated/leapmux/v1/agent_pb'
import type { TabMetadataStore } from '~/stores/tabMetadata.store'
import { generateSlug } from 'random-word-slugs'
import { createMemo, createSignal, Show } from 'solid-js'
import { workspaceClient } from '~/api/clients'
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
import { TabType } from '~/generated/leapmux/v1/workspace_pb'
import { createDirectoryTreeState } from '~/hooks/createDirectoryTreeState'
import { createSessionIdState } from '~/hooks/createSessionIdState'
import { useAgentProviderSelection } from '~/hooks/useAgentProviderSelection'
import { useWorkerDialog } from '~/hooks/useWorkerDialog'
import { seedTabIntoNewWorkspace } from '~/lib/crdt'
import { sanitizeName } from '~/lib/validate'
import { protoToAgentTabFields } from '~/stores/tab.helpers'
import { errorText } from '~/styles/shared.css'

interface NewWorkspaceDialogProps {
  onCreated: (workspaceId: string) => void
  onClose: () => void
  preselectedWorkerId?: string
  availableProviders?: AgentProvider[]
  onRefreshProviders?: () => void
  /**
   * Tab metadata store. The new workspace's agent is written here the moment
   * `OpenAgent` returns, so its tab renders with a title and provider as soon
   * as the projection places it — rather than appearing as a bare row until
   * the hydrator's `listAgents` round-trip lands ("Agent not found").
   */
  metadata: TabMetadataStore
}

export const NewWorkspaceDialog: Component<NewWorkspaceDialogProps> = (props) => {
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
      const agentResp = await workerRpc.openAgent(wid, {
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
        // Seed the agent's METADATA first, BEFORE the placement below -- the
        // same order `openTabInFocusedTile` documents and every other open path
        // follows. Placement is what makes the tab exist for the projection, and
        // it applies synchronously; patching afterwards (with an `await` in
        // between, no less) means the tab renders untitled and provider-less for
        // at least a microtask. The sidebar tree caches its grouping across
        // metadata-only changes, so that window used to be enough to leave the
        // row showing the bare "Agent" label and the generic bot icon until an
        // unrelated tab forced a rebuild.
        //
        // This has to happen here at all because `agentResp.agent` is the only
        // place this client has the title / provider / git fields; the worker
        // fan-out would otherwise be the first to supply them, and until it
        // landed the tab would render as a raw agent id.
        //
        // `hydrated: true` for the same reason every other local-open path
        // sets it: the OpenAgent response IS the worker's answer for this tab.
        // Without it the tab matches `useTabHydrators`' `!isHydrated` predicate
        // — which now runs over every workspace in the account, not just the
        // active one — and a `ListAgents` round-trip fires immediately for an
        // agent this client just created. That reply is applied raw, with none
        // of the live handler's in-flight-settings suppression, so a settings
        // edit made in the window before it lands is silently overwritten.
        props.metadata.patch(agentResp.agent.id, {
          ...protoToAgentTabFields(agentResp.agent.workerId, agentResp.agent),
          hydrated: true,
        })

        // After the worker has spawned the agent, wait for the
        // `WorkspaceCreated` event to populate `WorkspaceContentsRecord
        // .root_node_id` in the speculative state, then submit
        // `SetTabRegister(tile_id=root_node_id) + position + worker_id`
        // for the new agent. Without this, the agent exists on the
        // worker but is invisible to all clients via the CRDT
        // projection — they'd render an empty workspace until the
        // user touched another tab.
        // Awaited for its ops, not its return: it emits the placement batch.
        // The returned root/position used to seed a registry snapshot; the
        // projection now supplies both.
        await seedTabIntoNewWorkspace({
          workspaceId: wsResp.workspaceId,
          tabType: TabType.AGENT,
          tabId: agentResp.agent.id,
          workerId: wid,
        })
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
