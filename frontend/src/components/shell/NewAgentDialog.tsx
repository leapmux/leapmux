import type { Component } from 'solid-js'
import type { AgentInfo, AgentProvider } from '~/generated/leapmux/v1/agent_pb'
import { Show } from 'solid-js'
import * as workerRpc from '~/api/workerRpc'
import { DialogColumns, DialogTopRow, DialogTopSection } from '~/components/common/Dialog'
import { AgentProviderSelector } from '~/components/shell/AgentProviderSelector'
import { isAgentCreateDisabled } from '~/components/shell/dialogValidation'
import { DirectorySelector } from '~/components/shell/DirectorySelector'
import { GitOptions } from '~/components/shell/GitOptions'
import { GitOptionsLoader } from '~/components/shell/GitOptionsLoader'
import { SessionIdInput } from '~/components/shell/SessionIdInput'
import { DialogFormFooter, WorkerDialogShell } from '~/components/shell/WorkerDialogShell'
import { WorkerSelector } from '~/components/shell/WorkerSelector'
import { createDirectoryTreeState } from '~/hooks/createDirectoryTreeState'
import { createSessionIdState } from '~/hooks/createSessionIdState'
import { useAgentProviderSelection } from '~/hooks/useAgentProviderSelection'
import { useWorkerDialog } from '~/hooks/useWorkerDialog'

interface NewAgentDialogProps {
  workspaceId: string
  defaultWorkerId?: string
  defaultWorkingDir?: string
  defaultModel?: string
  defaultAgentProvider?: AgentProvider
  availableProviders?: AgentProvider[]
  onRefreshProviders?: () => void
  onCreated: (agent: AgentInfo) => void
  onClose: () => void
}

export const NewAgentDialog: Component<NewAgentDialogProps> = (props) => {
  const { submit: { submitting, error, formHandler }, worker, gitMode, pathInfo } = useWorkerDialog({
    submit: { fallback: 'Failed to create agent' },
    worker: {
      preselectedWorkerId: props.defaultWorkerId,
      defaultWorkingDir: props.defaultWorkingDir,
    },
    pathInfo: { remapWorktreeRoot: true },
  })
  const tree = createDirectoryTreeState()

  const { agentProvider, setAgentProvider, recordProviderUse, noProviders } = useAgentProviderSelection(
    () => props.availableProviders,
  )

  const sessionId = createSessionIdState()

  const submitDisabled = () => isAgentCreateDisabled({
    submitting: submitting.loading(),
    workspaceId: props.workspaceId,
    workerId: worker.workerId(),
    workingDir: worker.workingDir(),
    noProviders: noProviders(),
    sessionIdError: sessionId.error(),
    git: gitMode.currentIntent(),
  })

  const handleSubmit = formHandler(submitDisabled, async () => {
    const provider = agentProvider()
    // submitDisabled already guards on noProviders(), so reaching here
    // with provider===undefined would be a UI bug; throw explicitly so
    // an undefined value never silently rides the wire as proto enum 0.
    if (provider === undefined)
      throw new Error('No agent provider available')
    const resp = await workerRpc.openAgent(worker.workerId(), {
      workspaceId: props.workspaceId,
      agentProvider: provider,
      model: props.defaultModel ?? '',
      // title omitted: worker picks "Agent <Name>" from the shared pool.
      systemPrompt: '',
      workerId: worker.workerId(),
      workingDir: worker.workingDir(),
      ...gitMode.toGitFields(),
      ...(sessionId.trimmed() ? { agentSessionId: sessionId.trimmed() } : {}),
    })
    if (resp.agent) {
      recordProviderUse(provider)
      props.onCreated(resp.agent)
    }
  })

  return (
    <WorkerDialogShell
      title="New agent"
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
