import type { Component } from 'solid-js'
import type { AgentInfo, AgentProvider } from '~/generated/proto/leapmux/v1/agent_pb'
import type { createRepoGitStore } from '~/stores/repoGit.store'
import { createMemo, Show } from 'solid-js'
import * as workerRpc from '~/api/workerRpc'
import { openAgentRequestOptions } from '~/components/chat/providers/registry'
import { DialogColumns, DialogTopRow, DialogTopSection } from '~/components/common/Dialog'
import { AgentProviderSelector } from '~/components/shell/AgentProviderSelector'
import { BlockedReasonNotice } from '~/components/shell/BlockedReasonNotice'
import { isAgentCreateDisabled } from '~/components/shell/dialogValidation'
import { DirectorySelector } from '~/components/shell/DirectorySelector'
import { GitOptions } from '~/components/shell/GitOptions'
import { GitOptionsLoader } from '~/components/shell/GitOptionsLoader'
import { SessionIdInput } from '~/components/shell/SessionIdInput'
import { TitleInput } from '~/components/shell/TitleInput'
import { DialogFormFooter, WorkerDialogShell } from '~/components/shell/WorkerDialogShell'
import { WorkerSelector } from '~/components/shell/WorkerSelector'
import { createDirectoryTreeState } from '~/hooks/createDirectoryTreeState'
import { createSessionIdState } from '~/hooks/createSessionIdState'
import { createTitleState } from '~/hooks/createTitleState'
import { useAgentProviderSelection } from '~/hooks/useAgentProviderSelection'
import { GitMode } from '~/hooks/useGitModeState'
import { useWorkerDialog } from '~/hooks/useWorkerDialog'
import { randomAgentTitle } from '~/lib/tabTitles'

interface NewAgentDialogProps {
  defaultWorkerId?: string
  defaultWorkingDir?: string
  defaultAgentProvider?: AgentProvider
  availableProviders?: AgentProvider[]
  onRefreshProviders?: () => void
  /**
   * When this returns a string, no tab can be placed right now (no
   * workspace, or its tree has not arrived): submit is disabled and the
   * string is shown as the reason. Guards the worker RPC — creating the
   * agent first and refusing placement second would orphan the agent.
   */
  blockedReason?: () => string | undefined
  /**
   * `seedGitFromActiveTab` says whether the caller may copy the active tab's
   * branch onto the new tab while the worker's own status is on its way.
   *
   * Only the plain "use this directory" mode may. Every other mode redirects
   * the agent -- a new or existing worktree, or a different branch -- so the
   * active tab's branch would be the wrong answer for the directory the agent
   * actually lands in. This dialog is the only place that knows which mode ran.
   */
  onCreated: (agent: AgentInfo, opts: { seedGitFromActiveTab: boolean }) => void
  onClose: () => void
  repoGitStore: ReturnType<typeof createRepoGitStore>
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

  const sessionId = createSessionIdState(agentProvider)
  const title = createTitleState(randomAgentTitle)

  // One memo, two readers (submit gate + notice): `blockedReason` walks
  // the layout tree, and the submit computation re-runs on every field
  // keystroke — the memo keeps those walks to one per actual change.
  const blockedReason = createMemo(() => props.blockedReason?.())

  const submitDisabled = () => isAgentCreateDisabled({
    submitting: submitting.loading(),
    blockedReason: blockedReason(),
    workerId: worker.workerId(),
    workingDir: worker.workingDir(),
    noProviders: noProviders(),
    sessionIdError: sessionId.error(),
    titleError: title.error(),
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
      agentProvider: provider,
      // The CLEANED title: the worker applies the same rule to whatever
      // arrives, so sending the raw text would show one title here and store
      // another until the next refresh replaced it. The default came from the
      // same pool the worker would have drawn from.
      title: title.cleaned(),
      // Whether the user kept that default. Only this side can answer it --
      // the pre-filled `Agent <Name>` is indistinguishable from a typed one on
      // the wire -- and the worker records it so plan-mode auto-rename
      // overwrites a suggestion and never a title somebody chose.
      titleAutoGenerated: title.isPristine(),
      workerId: worker.workerId(),
      workingDir: worker.workingDir(),
      ...openAgentRequestOptions(provider),
      ...gitMode.toGitFields(),
      ...(sessionId.trimmed() ? { agentSessionId: sessionId.trimmed() } : {}),
    })
    if (resp.agent) {
      recordProviderUse(provider)
      props.onCreated(resp.agent, {
        seedGitFromActiveTab: gitMode.gitMode() === GitMode.Current,
      })
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
        <TitleInput state={title} />
      </DialogTopSection>
      <BlockedReasonNotice reason={blockedReason()} />
      <DialogColumns
        left={<DirectorySelector state={worker} tree={tree} repoGitStore={props.repoGitStore} />}
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
