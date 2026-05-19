import type { Component } from 'solid-js'
import { Show } from 'solid-js'
import * as workerRpc from '~/api/workerRpc'
import { DialogColumns, DialogTopRow, DialogTopSection } from '~/components/common/Dialog'
import { isTerminalCreateDisabled } from '~/components/shell/dialogValidation'
import { DirectorySelector } from '~/components/shell/DirectorySelector'
import { GitOptions } from '~/components/shell/GitOptions'
import { GitOptionsLoader } from '~/components/shell/GitOptionsLoader'
import { ShellSelect } from '~/components/shell/ShellSelect'
import { DialogFormFooter, WorkerDialogShell } from '~/components/shell/WorkerDialogShell'
import { WorkerSelector } from '~/components/shell/WorkerSelector'
import { useOrg } from '~/context/OrgContext'
import { createDirectoryTreeState } from '~/hooks/createDirectoryTreeState'
import { useAvailableShells } from '~/hooks/useAvailableShells'
import { useWorkerDialog } from '~/hooks/useWorkerDialog'
import { formatErrorMessage } from '~/lib/errors'
import { DEFAULT_TERMINAL_COLS, DEFAULT_TERMINAL_ROWS } from '~/lib/terminal'

interface NewTerminalDialogProps {
  workspaceId: string
  defaultWorkerId?: string
  defaultWorkingDir?: string
  onCreated: (terminalId: string, workerId: string, workingDir: string, title: string) => void
  onClose: () => void
}

export const NewTerminalDialog: Component<NewTerminalDialogProps> = (props) => {
  const org = useOrg()
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
      return { orgId: org.orgId(), workspaceId: props.workspaceId, workerId: id }
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

  const submitDisabled = () => isTerminalCreateDisabled({
    submitting: submitting.loading(),
    workspaceId: props.workspaceId,
    workerId: worker.workerId(),
    workingDir: worker.workingDir(),
    shell: shell(),
    git: gitMode.currentIntent(),
  })

  const handleSubmit = formHandler(submitDisabled, async () => {
    const resp = await workerRpc.openTerminal(worker.workerId(), {
      orgId: org.orgId(),
      workspaceId: props.workspaceId,
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
      <DialogColumns
        twoColumn={Boolean(worker.workerId()) && (pathInfo.loading() || pathInfo.showGitOptions())}
        left={<DirectorySelector state={worker} tree={tree} />}
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
