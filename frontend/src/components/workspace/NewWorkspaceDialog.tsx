import type { Component } from 'solid-js'
import type { WorkspaceStartPoint } from './workspaceStartPoint'
import type { WorkspaceCreationSubmission } from '~/components/shell/useTabCreation'
import type { AgentProvider } from '~/generated/proto/leapmux/v1/agent_pb'
import type { createRepoGitStore } from '~/stores/repoGit.store'
import { generateSlug } from 'random-word-slugs'
import { Show } from 'solid-js'
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
import { createDirectoryTreeState } from '~/hooks/createDirectoryTreeState'
import { createSessionIdState } from '~/hooks/createSessionIdState'
import { createTitleState } from '~/hooks/createTitleState'
import { useAgentProviderSelection } from '~/hooks/useAgentProviderSelection'
import { GitMode, initialIntentForMode } from '~/hooks/useGitModeState'
import { useWorkerDialog } from '~/hooks/useWorkerDialog'
import { formatErrorMessage } from '~/lib/errors'
import {
  gitModeStickyKey,
  readStickyGitMode,
  rememberStickyGitMode,
  startPointDialogSetup,
} from './workspaceStartPoint'

interface NewWorkspaceDialogProps {
  onCreate: (input: WorkspaceCreationSubmission) => Promise<boolean>
  onClose: () => void
  /**
   * Where the workspace starts from. Read ONCE at mount -- the dialog's `<Show>`
   * is keyed, so a different start point remounts the whole form rather than
   * mutating a half-filled one under the user.
   */
  startPoint: WorkspaceStartPoint
  availableProviders?: AgentProvider[]
  onRefreshProviders?: () => void
  repoGitStore: ReturnType<typeof createRepoGitStore>
}

export const NewWorkspaceDialog: Component<NewWorkspaceDialogProps> = (props) => {
  // Read once: `useWorkerDialog`'s sub-hooks take their seeds as plain
  // one-time values, and the keyed `<Show>` at the call site remounts this
  // component whenever the start point changes.
  // eslint-disable-next-line solid/reactivity -- one-time initial values
  const setup = startPointDialogSetup(props.startPoint)
  const seededMode = readStickyGitMode(setup.stickyKey)

  const { submit: { submitting, error, setError, formHandler }, worker, gitMode, pathInfo } = useWorkerDialog({
    submit: { fallback: 'Failed to create workspace' },
    worker: {
      ...(setup.workerId !== undefined
        ? { preselectedWorkerId: setup.workerId }
        : {}),
      ...(setup.workingDir !== undefined
        ? { defaultWorkingDir: setup.workingDir }
        : {}),
    },
    // The mode this repository was last started with, so the form opens on the
    // answer the user gave last time rather than on `Use current state`.
    // `GitOptions` clamps a seed outside its enabled set, and this dialog
    // enables all five, so any stored mode survives. The spread keeps
    // `gitMode` absent when no stored mode exists, rather than passing an
    // explicit undefined into an optional prop.
    ...(seededMode !== undefined
      ? {
          gitMode: {
            initialIntent: initialIntentForMode(seededMode),
          },
        }
      : {}),
    // A seeded snapshot, so `GitOptionsLoader` mounts `GitOptions` on the first
    // paint instead of showing the "Loading branch info" spinner while the
    // probe confirms what the menu already knew.
    ...(setup.pathInfoSeed !== undefined
      ? {
          pathInfo: { seed: setup.pathInfoSeed },
        }
      : {}),
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
    const provider = agentProvider()
    if (provider === undefined)
      throw new Error('No agent provider available')
    const workerId = worker.workerId()
    const workingDir = worker.workingDir()
    const rememberMode = pathInfo.showGitOptions()
    const mode = gitMode.gitMode()
    await props.onCreate({
      title: title.cleaned(),
      workerId,
      seedGitFromActiveTab: mode === GitMode.Current,
      request: {
        agentProvider: provider,
        workingDir,
        ...openAgentRequestOptions(provider),
        gitSetup: gitMode.toGitSetup(),
        ...(sessionId.trimmed()
          ? {
              agentSessionId: sessionId.trimmed(),
            }
          : {}),
      },
      onCommitted: () => {
        recordProviderUse(provider)
        if (rememberMode)
          rememberStickyGitMode(gitModeStickyKey(workerId, workingDir), mode)
      },
      onError: error => setError(formatErrorMessage(error, 'Failed to create workspace')),
    })
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
            {...(props.availableProviders !== undefined
              ? { availableProviders: props.availableProviders }
              : {})}
            {...(props.onRefreshProviders !== undefined
              ? { onRefresh: props.onRefreshProviders }
              : {})}
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
