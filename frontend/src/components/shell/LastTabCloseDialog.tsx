import type { Component } from 'solid-js'
import type { InspectLastTabCloseResponse } from '~/generated/proto/leapmux/v1/git_pb'
import type { TabType as TabTypeT } from '~/generated/proto/leapmux/v1/workspace_pb'
import { createMemo, createUniqueId, Show } from 'solid-js'
import * as workerRpc from '~/api/workerRpc'
import { actionsFooter } from '~/components/common/actionsFooter.css'
import { ConfirmButton } from '~/components/common/ConfirmButton'
import { Dialog } from '~/components/common/Dialog'
import { showWarnToast } from '~/components/common/Toast'
import { Tooltip } from '~/components/common/Tooltip'
import { BranchStatusInfo, hasPushableWork } from '~/components/workspace/BranchStatusInfo'
import { PushBranchButton } from '~/components/workspace/PushBranchButton'
import { LastTabCloseTarget } from '~/generated/proto/leapmux/v1/git_pb'
import { TabType } from '~/generated/proto/leapmux/v1/workspace_pb'
import { useWorkerHomeDir } from '~/hooks/useWorkerHomeDir'
import { createLogger } from '~/lib/logger'
import { flavorFromOs } from '~/lib/paths'
import { workerInfoStore } from '~/stores/workerInfo.store'
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
  // One id, shared by the visible reason and the button's description. See
  // the <Tooltip describedBy> prop for why the description must not be a
  // second copy of a sentence the dialog already shows.
  const blockedReasonId = `last-tab-blocked-reason-${createUniqueId()}`

  const isWorktree = () => props.state.target === LastTabCloseTarget.WORKTREE

  // One predicate for the Delete button and for the notice that explains why it
  // is unavailable, THROUGH `isWorktree()` like every other site. Reading the
  // raw field at each site let the two disagree: the button is a WORKTREE-target
  // control, so a reason arriving on any other target rendered a warning with no
  // button beside it.
  const removalBlockedReason = createMemo(() =>
    isWorktree() ? props.state.worktreeRemovalBlockedReason : '',
  )

  // The directory the prompt is about. `worktreePath` carries it on the
  // worktree target; on the branch target the checkout IS the main repo, so
  // `repoRoot` is the same directory. `workingDir` is NOT interchangeable --
  // the worker resolves it per tab and it can sit in a subdirectory.
  const directory = () => (isWorktree() ? props.state.worktreePath : props.state.repoRoot)
  // The worker's home dir and path flavor, for the tilde path. The hook keys
  // its fetch to the ID rather than to the mount, which matters here: this
  // dialog renders under a non-keyed `<Show>`, so a second `open()` for another
  // worker re-points this same instance without remounting it.
  const homeDir = useWorkerHomeDir(() => props.state.workerId)
  const flavor = () => {
    const os = workerInfoStore.getOs(props.state.workerId)
    return os ? flavorFromOs(os) : undefined
  }

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
  // dialog skipped the post-push safety check — surface the hint as a warn toast
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
        {/* The lead sentence states WHICH KIND of checkout this is about and
            nothing else. The rows below name it and give its directory, so an
            inline path here only printed the same string twice -- raw both
            times, because a sentence has no room to tilde-compress it. */}
        <p>
          <Show
            when={isWorktree()}
            fallback="This closes the last non-worktree tab for this branch."
          >
            This closes the last tab for this worktree.
          </Show>
        </p>
        <BranchStatusInfo
          branch={{
            isWorktree: isWorktree(),
            branchName: props.state.branchName,
            directory: directory(),
            homeDir: homeDir(),
            flavor: flavor(),
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
            // the user closes a FILE tab.
            willStop: props.state.tabType !== TabType.FILE,
          }}
        />
        {/* Why the Delete button below is unavailable. The worker's removal
            preflight rides on the same inspect response that opened this
            dialog, so it states the refusal while the tab is still open.
            After the tab closes, the removal runs unattended and no
            surface is left to report a refusal. This renders the reason as
            visible text, not only in the button's tooltip, because a
            greyed-out destructive option with no stated reason looks like
            a defect. */}
        <Show when={removalBlockedReason()}>
          <div id={blockedReasonId} class={warningText} data-testid="last-tab-blocked-reason">
            {removalBlockedReason()}
          </div>
        </Show>
        {/* The worker sets error_hint when a probe it needed did not answer.
            An empty blocked reason is also what a clean worktree sends, so
            without this the user cannot tell a removal git accepts from one
            nobody checked. */}
        <Show when={props.state.errorHint}>
          <div class={warningText}>{props.state.errorHint}</div>
        </Show>
      </section>
      <footer class={actionsFooter}>
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
        <Show when={isWorktree()}>
          {/* Disabled when git refuses the removal. "Close anyway" stays
              available, so the user still closes the tab -- only the
              removal is refused.

              The blocked reason goes through <Tooltip>; see that component's
              header for why no control here takes a `title`. The same text
              also renders in the body above, for anybody who never hovers --
              and `describedBy` points the description at THAT element, so the
              reason reaches the accessibility tree once rather than twice. */}
          <Tooltip text={removalBlockedReason() || undefined} describedBy={blockedReasonId}>
            <ConfirmButton
              data-variant="danger"
              disabled={Boolean(removalBlockedReason())}
              onClick={handleScheduleDelete}
            >
              Delete worktree
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
