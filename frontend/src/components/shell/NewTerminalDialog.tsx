import type { Component } from 'solid-js'
import LoaderCircle from 'lucide-solid/icons/loader-circle'
import { createEffect, createSignal, For, on, Show } from 'solid-js'
import { apiLoadingTimeoutMs } from '~/api/transport'
import * as workerRpc from '~/api/workerRpc'
import { Dialog, DialogColumns, DialogTopRow, DialogTopSection } from '~/components/common/Dialog'
import { Icon } from '~/components/common/Icon'
import { isTerminalCreateDisabled } from '~/components/shell/dialogValidation'
import { DirectorySelector } from '~/components/shell/DirectorySelector'
import { GitOptions } from '~/components/shell/GitOptions'
import { WorkerSelector } from '~/components/shell/WorkerSelector'
import { createLoadingSignal } from '~/hooks/createLoadingSignal'
import { createWorkerDialogState } from '~/hooks/createWorkerDialogState'
import { spinner } from '~/styles/animations.css'
import { errorText } from '~/styles/shared.css'

interface NewTerminalDialogProps {
  workspaceId: string
  defaultWorkerId?: string
  defaultWorkingDir?: string
  onCreated: (terminalId: string, workerId: string, workingDir: string) => void
  onClose: () => void
}

export const NewTerminalDialog: Component<NewTerminalDialogProps> = (props) => {
  const state = createWorkerDialogState({
    preselectedWorkerId: props.defaultWorkerId,
    defaultWorkingDir: props.defaultWorkingDir,
    resolveWorktree: true,
  })
  const [shells, setShells] = createSignal<string[]>([])
  const [defaultShell, setDefaultShell] = createSignal('')
  const [shell, setShell] = createSignal('')
  const [shellsLoading, setShellsLoading] = createSignal(false)
  const submitting = createLoadingSignal(apiLoadingTimeoutMs())

  // Fetch available shells when worker changes
  createEffect(on(() => state.workerId(), async (id) => {
    if (!id)
      return

    setShellsLoading(true)
    setShells([])
    setShell('')
    try {
      const resp = await workerRpc.listAvailableShells(id, {
        orgId: state.org.orgId(),
        workspaceId: props.workspaceId,
        workerId: id,
      })
      setShells(resp.shells)
      const def = resp.defaultShell || (resp.shells.length > 0 ? resp.shells[0] : '')
      setDefaultShell(def)
      setShell(def)
    }
    catch (e) {
      state.setError(e instanceof Error ? e.message : 'Failed to load shells')
    }
    finally {
      setShellsLoading(false)
    }
  }))

  const handleSubmit = async (e: Event) => {
    e.preventDefault()
    if (!props.workspaceId || !state.workerId() || !state.workingDir().trim() || !shell())
      return

    submitting.start()
    state.setError(null)
    try {
      const resp = await workerRpc.openTerminal(state.workerId(), {
        orgId: state.org.orgId(),
        workspaceId: props.workspaceId,
        cols: 80,
        rows: 24,
        workingDir: state.workingDir(),
        shell: shell(),
        workerId: state.workerId(),
        createWorktree: state.gitMode() === 'create-worktree',
        worktreeBranch: state.worktreeBranch(),
        worktreeBaseBranch: state.gitMode() === 'create-worktree' ? state.worktreeBaseBranch() : '',
        checkoutBranch: state.gitMode() === 'switch-branch' ? state.checkoutBranch() : '',
        createBranch: state.gitMode() === 'create-branch' ? state.createBranch() : '',
        createBranchBase: state.gitMode() === 'create-branch' ? state.createBranchBase() : '',
        useWorktreePath: state.gitMode() === 'use-worktree' ? state.useWorktreePath() : '',
      })
      props.onCreated(resp.terminalId, state.workerId(), state.workingDir())
    }
    catch (err) {
      state.setError(err instanceof Error ? err.message : 'Failed to create terminal')
    }
    finally {
      submitting.stop()
    }
  }

  const shellSelector = () => (
    <label>
      Shell
      <Show
        when={!shellsLoading() && shells().length > 0}
        fallback={(
          <select disabled>
            <option value="">{shellsLoading() ? 'Loading shells...' : 'No shells available'}</option>
          </select>
        )}
      >
        <select
          value={shell()}
          onChange={e => setShell(e.currentTarget.value)}
        >
          <For each={shells()}>
            {s => (
              <option value={s}>
                {s === defaultShell() ? `${s} (default)` : s}
              </option>
            )}
          </For>
        </select>
      </Show>
    </label>
  )

  return (
    <Dialog title="New Terminal" tall wide busy={submitting.loading()} onClose={() => props.onClose()}>
      <form onSubmit={handleSubmit}>
        <section>
          <div class="vstack gap-4">
            <DialogTopSection>
              <DialogTopRow>
                <WorkerSelector state={state} />
                {shellSelector()}
              </DialogTopRow>
            </DialogTopSection>
            <DialogColumns
              twoColumn={state.showGitOptions()}
              left={<DirectorySelector state={state} />}
              right={(
                <Show when={state.workerId() && !state.worktreeResolving()}>
                  <GitOptions
                    workerId={state.workerId()}
                    selectedPath={state.workingDir()}
                    homeDir={state.workerInfoStore.getHomeDir(state.workerId())}
                    refreshKey={state.refreshKey()}
                    onGitModeChange={state.handleGitModeChange}
                    onVisibilityChange={state.setShowGitOptions}
                  />
                </Show>
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
            disabled={isTerminalCreateDisabled({ submitting: submitting.loading(), workspaceId: props.workspaceId, workerId: state.workerId(), workingDir: state.workingDir(), shell: shell(), gitMode: state.gitMode(), worktreeBranchError: state.worktreeBranchError(), checkoutBranch: state.checkoutBranch(), createBranchError: state.createBranchError(), useWorktreePath: state.useWorktreePath() })}
          >
            <Show when={submitting.loading()}><Icon icon={LoaderCircle} size="sm" class={spinner} /></Show>
            {submitting.loading() ? 'Creating...' : 'Create'}
          </button>
        </footer>
      </form>
    </Dialog>
  )
}
