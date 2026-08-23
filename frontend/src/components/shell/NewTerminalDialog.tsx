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
import { ShellSelect } from '~/components/shell/ShellSelect'
import { DialogFormFooter, WorkerDialogShell } from '~/components/shell/WorkerDialogShell'
import { WorkerSelector } from '~/components/shell/WorkerSelector'
import { createDirectoryTreeState } from '~/hooks/createDirectoryTreeState'
import { useAvailableShells } from '~/hooks/useAvailableShells'
import { useWorkerDialog } from '~/hooks/useWorkerDialog'
import { formatErrorMessage } from '~/lib/errors'
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
  onCreated: (terminalId: string, workerId: string, workingDir: string, title: string) => void
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
  const { shells, defaultShell, shell, setShell, loading: shellsLoading } = useAvailableShells(
    () => {
      const id = worker.workerId()
      if (!id)
        return null
      return { workerId: id }
    },
    err => setError(formatErrorMessage(err, 'Failed to load shells')),
  )

  const shellSelector = () => (
    <label>
      Shell
      <ShellSelect
        value={shell()}
        onChange={setShell}
        shells={shells()}
        defaultShell={defaultShell()}
        loading={shellsLoading()}
      />
    </label>
  )

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
    git: gitMode.currentIntent(),
  })

  const handleSubmit = formHandler(submitDisabled, async () => {
    const resp = await workerRpc.openTerminal(worker.workerId(), {
      cols: DEFAULT_TERMINAL_COLS,
      rows: DEFAULT_TERMINAL_ROWS,
      workingDir: worker.workingDir(),
      shell: shell(),
      workerId: worker.workerId(),
      ...gitMode.toGitFields(),
    })
    props.onCreated(resp.terminalId, worker.workerId(), worker.workingDir(), resp.title)
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
          {shellSelector()}
        </DialogTopRow>
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
