import type { Component } from 'solid-js'
import type { AgentProvider } from '~/generated/proto/leapmux/v1/agent_pb'
import type { createRepoGitStore } from '~/stores/repoGit.store'
import type { TabMetadataStore } from '~/stores/tabMetadata.store'
import { generateSlug } from 'random-word-slugs'
import { Show } from 'solid-js'
import { workspaceClient } from '~/api/clients'
import * as workerRpc from '~/api/workerRpc'
import { openAgentRequestOptions } from '~/components/chat/providers/registry'
import { DialogColumns, DialogTopRow, DialogTopSection } from '~/components/common/Dialog'
import { AgentProviderSelector } from '~/components/shell/AgentProviderSelector'
import { isAgentCreateDisabled } from '~/components/shell/dialogValidation'
import { DirectorySelector } from '~/components/shell/DirectorySelector'
import { GitOptions } from '~/components/shell/GitOptions'
import { GitOptionsLoader } from '~/components/shell/GitOptionsLoader'
import { ResumeSessionField } from '~/components/shell/ResumeSessionField'
import { TitleInput } from '~/components/shell/TitleInput'
import { DialogFormFooter, WorkerDialogShell } from '~/components/shell/WorkerDialogShell'
import { WorkerSelector } from '~/components/shell/WorkerSelector'
import { TabType } from '~/generated/proto/leapmux/v1/workspace_pb'
import { createDirectoryTreeState } from '~/hooks/createDirectoryTreeState'
import { createSessionIdState } from '~/hooks/createSessionIdState'
import { createTitleState } from '~/hooks/createTitleState'
import { useAgentProviderSelection } from '~/hooks/useAgentProviderSelection'
import { useWorkerDialog } from '~/hooks/useWorkerDialog'
import { seedTabIntoNewWorkspace } from '~/lib/crdt'
import { openedAgentTabFields } from '~/stores/tab.helpers'

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
  repoGitStore: ReturnType<typeof createRepoGitStore>
}

export const NewWorkspaceDialog: Component<NewWorkspaceDialogProps> = (props) => {
  const { submit: { submitting, error, formHandler }, worker, gitMode, pathInfo } = useWorkerDialog({
    submit: { fallback: 'Failed to create workspace' },
    // eslint-disable-next-line solid/reactivity -- one-time initial value
    worker: { preselectedWorkerId: props.preselectedWorkerId },
  })
  const tree = createDirectoryTreeState()
  // A word slug, not the worker's tab-name pool: a workspace carries no
  // "Agent "/"Terminal " prefix, so a lone pooled first name would read as an
  // unfinished label.
  const title = createTitleState(() => generateSlug(3, { format: 'title' }))

  const { agentProvider, setAgentProvider, recordProviderUse, noProviders } = useAgentProviderSelection(
    () => props.availableProviders,
  )

  const sessionId = createSessionIdState(agentProvider)

  const submitDisabled = () => isAgentCreateDisabled({
    submitting: submitting.loading(),
    workerId: worker.workerId(),
    workingDir: worker.workingDir(),
    noProviders: noProviders(),
    titleError: title.error(),
    sessionIdError: sessionId.error(),
    git: gitMode.currentIntent(),
  })

  const handleSubmit = formHandler(submitDisabled, async () => {
    let createdWorkspaceId: string | undefined
    try {
      const wsResp = await workspaceClient.createWorkspace({
        // The CLEANED title, not the raw one -- `createTitleState.cleaned`
        // documents why the two must not differ.
        title: title.cleaned(),
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
        // `openedAgentTabFields` carries `hydrated: true`, for the same reason
        // every other local-open path needs it: the OpenAgent response IS the
        // worker's answer for this tab. Without it the tab matches
        // `useTabHydrators`' `!isHydrated` predicate — which now runs over every
        // workspace in the account, not just the active one — and a `ListAgents`
        // round-trip fires immediately for an agent this client just created.
        // That reply is applied raw, with none of the live handler's
        // in-flight-settings suppression, so a settings edit made in the window
        // before it lands is silently overwritten.
        props.metadata.patch(agentResp.agent.id, openedAgentTabFields(props.repoGitStore, agentResp.agent))

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
          // The RESPONSE's worker id, not the local `wid` the RPC was routed
          // to. `tab.workerId` comes from this projection register, and it is
          // half of the repo key the sidebar reads a tab's branch with. The
          // other half comes from the response. One identity, one source, so
          // the two cannot disagree. The other two open paths already pass
          // `agent.workerId` here.
          workerId: agentResp.agent.workerId,
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
        <TitleInput state={title} />
      </DialogTopSection>
      <DialogColumns
        left={<DirectorySelector state={worker} tree={tree} repoGitStore={props.repoGitStore} />}
        right={(
          <>
            <ResumeSessionField
              state={sessionId}
              workerId={worker.workerId()}
              workingDir={worker.workingDir()}
              agentProvider={agentProvider()}
            />
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
