import type { Component } from 'solid-js'
import type { BranchGitState } from '~/generated/leapmux/v1/git_pb'
import type { TabType } from '~/generated/leapmux/v1/workspace_pb'
import { Show } from 'solid-js'
import * as workerRpc from '~/api/workerRpc'
import { Spinner } from '~/components/common/Spinner'
import { showInfoToast, showWarnToast } from '~/components/common/Toast'
import { useDialogSubmit } from '~/hooks/useDialogSubmit'

interface PushBranchButtonProps {
  workerId: string
  /** Any tab in the branch group — they all share the working dir. */
  tab: { type: TabType, id: string }
  /**
   * Fresh inspect snapshot. Drives the button label only
   * ("Commit and Push" vs. "Push"). The server-side push always re-probes
   * dirty-tree and pushStatusForPath — a sibling actor (terminal, IDE)
   * can mutate state between inspect and push, and trusting a cached
   * snapshot there was the source of past bugs.
   */
  gitState: BranchGitState | undefined
  /**
   * Called after the push succeeds. Use it to refresh any local
   * inspect/state so the dialog reflects the post-push status.
   */
  onPushed: () => Promise<void> | void
  /** Set to true while a sibling action is in flight. */
  disabled?: boolean
}

export const PushBranchButton: Component<PushBranchButtonProps> = (props) => {
  // useDialogSubmit owns the spinner + try/catch/finally. The `onError`
  // callback receives the raw `err` so the toast helper can extract its
  // own message — the dialog has no inline error slot for this button.
  const { submitting, run } = useDialogSubmit({
    onError: err => showWarnToast('Failed to push branch', err),
  })

  const handleClick = () => {
    void run(async () => {
      await workerRpc.pushBranch(props.workerId, {
        tabType: props.tab.type,
        tabId: props.tab.id,
      })
      await props.onPushed()
      showInfoToast('Branch pushed successfully')
    })
  }

  return (
    <button type="button" onClick={handleClick} disabled={submitting.loading() || props.disabled}>
      {props.gitState?.hasUncommittedChanges ? 'Commit and Push' : 'Push'}
      <Show when={submitting.loading()}><Spinner /></Show>
    </button>
  )
}
