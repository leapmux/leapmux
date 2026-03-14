import type { Component } from 'solid-js'
import LoaderCircle from 'lucide-solid/icons/loader-circle'
import { createEffect, createSignal, For, on, Show } from 'solid-js'
import { apiLoadingTimeoutMs } from '~/api/transport'
import * as workerRpc from '~/api/workerRpc'
import { Dialog } from '~/components/common/Dialog'
import { Icon } from '~/components/common/Icon'
import { isTerminalCreateDisabled } from '~/components/shell/dialogValidation'
import { DirectorySelector } from '~/components/shell/DirectorySelector'
import { GitOptions } from '~/components/shell/GitOptions'
import { WorkerSelector } from '~/components/shell/WorkerSelector'
import { createLoadingSignal } from '~/hooks/createLoadingSignal'
import { createWorkerDialogState } from '~/hooks/createWorkerDialogState'
import { spinner } from '~/styles/animations.css'
import { dialogLeftPanel, dialogRightPanel, dialogTwoColumn, errorText } from '~/styles/shared.css'

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
      setShell(resp.defaultShell || (resp.shells.length > 0 ? resp.shells[0] : ''))
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
    if (!state.workerId() || !state.workingDir().trim() || !shell())
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
      <select
        value={shell()}
        onChange={e => setShell(e.currentTarget.value)}
        disabled={shellsLoading() || shells().length === 0}
      >
        <Show when={shellsLoading()}>
          <option value="">Loading shells...</option>
        </Show>
        <Show when={!shellsLoading() && shells().length === 0}>
          <option value="">No shells available</option>
        </Show>
        <For each={shells()}>
          {s => <option value={s}>{s}</option>}
        </For>
      </select>
    </label>
  )

  const leftContent = () => (
    <>
      <WorkerSelector state={state} />
      <DirectorySelector state={state} />
      {shellSelector()}
    </>
  )

  const gitOptionsEl = () => (
    <Show when={state.workerId()}>
      <GitOptions
        workerId={state.workerId()}
        selectedPath={state.workingDir()}
        homeDir={state.workerInfoStore.getHomeDir(state.workerId())}
        onGitModeChange={state.handleGitModeChange}
        onVisibilityChange={state.setShowGitOptions}
      />
    </Show>
  )

  return (
    <Dialog title="New Terminal" tall onClose={() => props.onClose()}>
      <form onSubmit={handleSubmit}>
        <section>
          <Show
            when={state.showGitOptions()}
            fallback={(
              <div class="vstack gap-4">
                {leftContent()}
                {gitOptionsEl()}
                <Show when={state.error()}>
                  <div class={errorText}>{state.error()}</div>
                </Show>
              </div>
            )}
          >
            <div class={dialogTwoColumn}>
              <div class={dialogLeftPanel}>
                {leftContent()}
              </div>
              <div class={dialogRightPanel}>
                {gitOptionsEl()}
              </div>
            </div>
            <Show when={state.error()}>
              <div class={errorText}>{state.error()}</div>
            </Show>
          </Show>
        </section>
        <footer>
          <button type="button" class="outline" onClick={() => props.onClose()}>
            Cancel
          </button>
          <button
            type="submit"
            disabled={isTerminalCreateDisabled({ submitting: submitting.loading(), workerId: state.workerId(), workingDir: state.workingDir(), shell: shell(), gitMode: state.gitMode(), worktreeBranchError: state.worktreeBranchError(), checkoutBranch: state.checkoutBranch(), useWorktreePath: state.useWorktreePath() })}
          >
            <Show when={submitting.loading()}><Icon icon={LoaderCircle} size="sm" class={spinner} /></Show>
            {submitting.loading() ? 'Creating...' : 'Create'}
          </button>
        </footer>
      </form>
    </Dialog>
  )
}
