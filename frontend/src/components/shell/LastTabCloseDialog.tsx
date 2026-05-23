import type { Component } from 'solid-js'
import type { InspectLastTabCloseResponse } from '~/generated/leapmux/v1/git_pb'
import type { TabType as TabTypeT } from '~/generated/leapmux/v1/workspace_pb'
import { Show } from 'solid-js'
import * as workerRpc from '~/api/workerRpc'
import { ConfirmButton } from '~/components/common/ConfirmButton'
import { Dialog } from '~/components/common/Dialog'
import { BranchStatusInfo, hasPushableWork } from '~/components/workspace/BranchStatusInfo'
import { PushBranchButton } from '~/components/workspace/PushBranchButton'
import { LastTabCloseTarget } from '~/generated/leapmux/v1/git_pb'
import { TabType } from '~/generated/leapmux/v1/workspace_pb'
import { createLogger } from '~/lib/logger'

const log = createLogger('LastTabCloseDialog')

export type LastTabCloseChoice = 'cancel' | 'schedule-delete' | 'close-anyway'

export interface LastTabConfirmState extends InspectLastTabCloseResponse {
  workerId: string
  tabId: string
  tabType: TabTypeT
  resolve: (choice: LastTabCloseChoice) => void
}

export interface LastTabCloseDialogProps {
  state: LastTabConfirmState
  /** Called after resolve() to clear the dialog from the parent. */
  onDismiss: () => void
  /**
   * Notified after a successful PushBranch with the refreshed
   * inspectLastTabClose payload (diff/unpushed/can_push may have
   * changed). The parent owns `state` so it must merge the new fields
   * back into its LastTabConfirmState; the dialog renders directly off
   * `props.state` and updates re-render automatically.
   */
  onStatusRefreshed?: (status: InspectLastTabCloseResponse) => void
}

// Confirmation dialog rendered when closing the last tab for a worktree
// or for a branch with pending git state (dirty tree, unpushed commits).
// Offers Push / schedule-delete (worktree) / close-anyway alongside Cancel.
export const LastTabCloseDialog: Component<LastTabCloseDialogProps> = (props) => {
  const handleCancel = () => {
    props.state.resolve('cancel')
    props.onDismiss()
  }

  // Failures here must not propagate: the push already succeeded, so a
  // rejection from inspectLastTabClose would surface a misleading
  // "Failed to push branch" toast (via PushBranchButton's useDialogSubmit
  // onError) and skip the success toast. Treat the refresh as best-effort.
  const refreshStatus = async () => {
    try {
      const updated = await workerRpc.inspectLastTabClose(props.state.workerId, {
        tabType: props.state.tabType,
        tabId: props.state.tabId,
      })
      props.onStatusRefreshed?.(updated)
    }
    catch (err) {
      log.warn('inspectLastTabClose after push failed', err)
    }
  }

  const handleScheduleDelete = () => {
    props.state.resolve('schedule-delete')
    props.onDismiss()
  }
  const handleCloseAnyway = () => {
    props.state.resolve('close-anyway')
    props.onDismiss()
  }

  return (
    <Dialog title="Close last tab" onClose={handleCancel}>
      <section>
        <p>
          <Show
            when={props.state.target === LastTabCloseTarget.WORKTREE}
            fallback={(
              <>
                You are closing the last non-worktree tab for branch
                {' '}
                <code>{props.state.branchName}</code>
                .
              </>
            )}
          >
            You are closing the last tab for worktree
            {' '}
            <code>{props.state.worktreePath}</code>
            .
          </Show>
        </p>
        <BranchStatusInfo
          branch={{
            isWorktree: props.state.target === LastTabCloseTarget.WORKTREE,
            worktreePath: props.state.worktreePath,
            branchName: props.state.branchName,
            gitState: props.state.gitState,
          }}
          affectedTabs={{
            agents: props.state.tabType === TabType.AGENT ? 1 : 0,
            terminals: props.state.tabType === TabType.TERMINAL ? 1 : 0,
            files: props.state.tabType === TabType.FILE ? 1 : 0,
            // AGENT / TERMINAL closes stop a running process; FILE
            // closes only drop a viewer. The dialog uses willStop to
            // pick the verb ("will be stopped" vs. "will keep running"),
            // and a closed FILE tab is just gone — there's no process
            // to stop or keep — so the more accurate phrasing is
            // "will keep running" for the (zero) agents/terminals when
            // a FILE tab is the one being closed.
            willStop: props.state.tabType !== TabType.FILE,
          }}
        />
      </section>
      <footer>
        <button type="button" class="outline" onClick={handleCancel}>
          Cancel
        </button>
        <Show when={hasPushableWork(props.state.gitState)}>
          <PushBranchButton
            workerId={props.state.workerId}
            tab={{ type: props.state.tabType, id: props.state.tabId }}
            gitState={props.state.gitState}
            onPushed={refreshStatus}
          />
        </Show>
        <Show when={props.state.target === LastTabCloseTarget.WORKTREE}>
          <ConfirmButton data-variant="danger" onClick={handleScheduleDelete}>
            Delete
          </ConfirmButton>
        </Show>
        <ConfirmButton data-variant="danger" onClick={handleCloseAnyway}>
          Close anyway
        </ConfirmButton>
      </footer>
    </Dialog>
  )
}
