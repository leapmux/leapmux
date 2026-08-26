import type { Component } from 'solid-js'
import type { InspectLastTabCloseResponse } from '~/generated/leapmux/v1/git_pb'
import type { TabType as TabTypeT } from '~/generated/leapmux/v1/workspace_pb'
import { createMemo, Show } from 'solid-js'
import * as workerRpc from '~/api/workerRpc'
import { ConfirmButton } from '~/components/common/ConfirmButton'
import { Dialog } from '~/components/common/Dialog'
import { showWarnToast } from '~/components/common/Toast'
import { Tooltip } from '~/components/common/Tooltip'
import { BranchStatusInfo, hasPushableWork } from '~/components/workspace/BranchStatusInfo'
import { PushBranchButton } from '~/components/workspace/PushBranchButton'
import { LastTabCloseTarget } from '~/generated/leapmux/v1/git_pb'
import { TabType } from '~/generated/leapmux/v1/workspace_pb'
import { createLogger } from '~/lib/logger'
import { warningText } from '~/styles/shared.css'

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
  // One predicate for the Delete button and for the notice that explains why it
  // is unavailable. Reading the raw field at each site let the two disagree: the
  // button is a WORKTREE-target control, so a reason arriving on any other
  // target rendered a warning with no button beside it.
  const removalBlockedReason = createMemo(() =>
    props.state.target === LastTabCloseTarget.WORKTREE ? props.state.worktreeRemovalBlockedReason : '',
  )

  const handleCancel = () => {
    props.state.resolve('cancel')
    props.onDismiss()
  }

  // Failures here must not propagate: the push already succeeded, so a
  // rejection from inspectLastTabClose would surface a misleading
  // "Failed to push branch" toast (via PushBranchButton's useDialogSubmit
  // onError) and skip the success toast. Treat the refresh as best-effort.
  //
  // A degraded refresh (worktree vanished, git broke) returns a response
  // with shouldPrompt=false + errorHint instead of rejecting. The dialog
  // stays open so the user still sees the pre-push branch state, but the
  // post-push safety check was skipped — surface the hint as a warn toast
  // so the user isn't silently left looking at stale state. Mirrors
  // useTabOperations.handleTabClose's error_hint handling.
  const refreshStatus = async () => {
    try {
      const updated = await workerRpc.inspectLastTabClose(props.state.workerId, {
        tabType: props.state.tabType,
        tabId: props.state.tabId,
      })
      if (updated.errorHint) {
        showWarnToast(updated.errorHint)
      }
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
        {/* Why the Delete button below is unavailable. The worker's removal
            preflight rides on the same inspect response that opened this
            dialog, so it states the refusal while the tab is still open.
            After the tab closes, the removal runs unattended and no
            surface is left to report a refusal. This renders the reason as
            visible text, not only as the button's `title`, because a
            greyed-out destructive option with no stated reason looks like
            a defect. */}
        <Show when={removalBlockedReason()}>
          <div class={warningText}>{removalBlockedReason()}</div>
        </Show>
        {/* The worker sets error_hint when a probe it needed did not answer.
            An empty blocked reason is also what a clean worktree sends, so
            without this the user cannot tell a removal git accepts from one
            nobody checked. */}
        <Show when={props.state.errorHint}>
          <div class={warningText}>{props.state.errorHint}</div>
        </Show>
      </section>
      <footer>
        <button type="button" class="outline" onClick={handleCancel}>
          Cancel
        </button>
        <Show when={hasPushableWork(props.state.gitState)}>
          {/* `workingDir` is the dir the worker itself resolved for this tab
              and echoed back on the inspect response, so the push runs in
              exactly the repo this prompt is about. */}
          <PushBranchButton
            workerId={props.state.workerId}
            workingDir={props.state.workingDir}
            gitState={props.state.gitState}
            onPushed={refreshStatus}
          />
        </Show>
        <Show when={props.state.target === LastTabCloseTarget.WORKTREE}>
          {/* Disabled when git refuses the removal. "Close anyway" stays
              available, so the user still closes the tab -- only the
              removal is refused.

              The blocked reason goes through <Tooltip>, which works on a
              disabled control and leaves the button its own name. On `title`
              the reason BECOMES that name, and this button's name is STATE
              ("Confirm?" once armed) -- so the reason replaced the one thing
              the name had to carry, and a role+name lookup for "Delete"
              stopped matching. The same text also renders in the body above,
              for anybody who never hovers. */}
          <Tooltip text={removalBlockedReason() || undefined}>
            <ConfirmButton
              data-variant="danger"
              disabled={Boolean(removalBlockedReason())}
              onClick={handleScheduleDelete}
            >
              Delete
            </ConfirmButton>
          </Tooltip>
        </Show>
        <ConfirmButton data-variant="danger" onClick={handleCloseAnyway}>
          Close anyway
        </ConfirmButton>
      </footer>
    </Dialog>
  )
}
