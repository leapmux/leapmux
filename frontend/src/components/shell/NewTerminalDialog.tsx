import type { Component } from 'solid-js'
import type { createRepoGitStore } from '~/stores/repoGit.store'
import { createMemo, Show } from 'solid-js'
import * as workerRpc from '~/api/workerRpc'
import { DialogColumns, DialogTopRow, DialogTopSection } from '~/components/common/Dialog'
import { BlockedReasonNotice } from '~/components/shell/BlockedReasonNotice'
import { isTerminalCreateDisabled } from '~/components/shell/dialogValidation'
import { DirectorySelector } from '~/components/shell/DirectorySelector'
import { GitOptions } from '~/components/shell/GitOptions'
import { GitOptionsLoader } from '~/components/shell/GitOptionsLoader'
import { ShellSelector } from '~/components/shell/ShellSelector'
import { TitleInput } from '~/components/shell/TitleInput'
import { DialogFormFooter, WorkerDialogShell } from '~/components/shell/WorkerDialogShell'
import { WorkerSelector } from '~/components/shell/WorkerSelector'
import { createDirectoryTreeState } from '~/hooks/createDirectoryTreeState'
import { createTitleState } from '~/hooks/createTitleState'
import { useAvailableShells } from '~/hooks/useAvailableShells'
import { GitMode } from '~/hooks/useGitModeState'
import { useWorkerDialog } from '~/hooks/useWorkerDialog'
import { formatErrorMessage } from '~/lib/errors'
import { randomTerminalTitle } from '~/lib/tabTitles'
import { DEFAULT_TERMINAL_COLS, DEFAULT_TERMINAL_ROWS } from '~/lib/terminal'

interface NewTerminalDialogProps {
  defaultWorkerId?: string
  defaultWorkingDir?: string
  /**
   * When this returns a string, no tab can be placed right now (no
   * workspace, or its tree has not arrived): submit is disabled and the
   * string is shown as the reason. Guards the worker RPC — creating the
   * pty first and refusing placement second would orphan it.
   */
  blockedReason?: () => string | undefined
  /**
   * `seedGitFromActiveTab` says whether the caller may copy the active tab's
   * branch onto the new tab until the worker's first status arrives.
   *
   * Only the plain "use this directory" mode may. Every other mode redirects
   * the terminal into a worktree or onto another branch, where the active tab's
   * branch is the wrong answer for the directory it lands in.
   */
  onCreated: (
    terminalId: string,
    workerId: string,
    workingDir: string,
    title: string,
    opts: { seedGitFromActiveTab: boolean },
  ) => void
  onClose: () => void
  repoGitStore: ReturnType<typeof createRepoGitStore>
}

export const NewTerminalDialog: Component<NewTerminalDialogProps> = (props) => {
  const { submit: { submitting, error, setError, formHandler }, worker, gitMode, pathInfo } = useWorkerDialog({
    submit: { fallback: 'Failed to create terminal' },
    worker: {
      preselectedWorkerId: props.defaultWorkerId,
      defaultWorkingDir: props.defaultWorkingDir,
    },
    pathInfo: { remapWorktreeRoot: true },
  })
  const tree = createDirectoryTreeState()
  const shellState = useAvailableShells(
    () => {
      const id = worker.workerId()
      if (!id)
        return null
      return { workerId: id }
    },
    err => setError(formatErrorMessage(err, 'Failed to load shells')),
  )
  const { shell } = shellState
  const title = createTitleState(randomTerminalTitle)

  // One memo, two readers (submit gate + notice): `blockedReason` walks
  // the layout tree, and the submit computation re-runs on every field
  // keystroke — the memo keeps those walks to one per actual change.
  const blockedReason = createMemo(() => props.blockedReason?.())

  const submitDisabled = () => isTerminalCreateDisabled({
    submitting: submitting.loading(),
    blockedReason: blockedReason(),
    workerId: worker.workerId(),
    workingDir: worker.workingDir(),
    shell: shell(),
    titleError: title.error(),
    git: gitMode.currentIntent(),
  })

  const handleSubmit = formHandler(submitDisabled, async () => {
    const resp = await workerRpc.openTerminal(worker.workerId(), {
      cols: DEFAULT_TERMINAL_COLS,
      rows: DEFAULT_TERMINAL_ROWS,
      workingDir: worker.workingDir(),
      shell: shell(),
      workerId: worker.workerId(),
      // The CLEANED title: the worker applies the same rule to whatever
      // arrives, so sending the raw text would show one title here and store
      // another until the next refresh replaced it.
      title: title.cleaned(),
      ...gitMode.toGitFields(),
    })
    props.onCreated(resp.terminalId, worker.workerId(), worker.workingDir(), resp.title, {
      seedGitFromActiveTab: gitMode.gitMode() === GitMode.Current,
    })
  })

  return (
    <WorkerDialogShell
      title="New terminal"
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
          <ShellSelector state={shellState} />
        </DialogTopRow>
        <TitleInput state={title} placeholder="New Terminal" />
      </DialogTopSection>
      <BlockedReasonNotice reason={blockedReason()} />
      <DialogColumns
        twoColumn={Boolean(worker.workerId()) && (pathInfo.loading() || pathInfo.showGitOptions())}
        left={<DirectorySelector state={worker} tree={tree} repoGitStore={props.repoGitStore} />}
        right={(
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
        )}
      />
    </WorkerDialogShell>
  )
}
