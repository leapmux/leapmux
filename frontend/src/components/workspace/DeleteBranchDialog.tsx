import type { Component } from 'solid-js'
import type { WorktreeCloseSummary } from '~/components/shell/closeResultToast'
import type { InspectBranchDeletionResponse } from '~/generated/leapmux/v1/git_pb'
import type { Tab } from '~/stores/tab.types'
import { createMemo, createSignal, Show } from 'solid-js'
import * as workerRpc from '~/api/workerRpc'
import { ConfirmButton } from '~/components/common/ConfirmButton'
import { labelRow } from '~/components/common/Dialog.css'
import { Spinner } from '~/components/common/Spinner'
import { showInfoToast, showWarnToast } from '~/components/common/Toast'
import { WorkerDialogShell } from '~/components/shell/WorkerDialogShell'
import { BranchSelect, partitionBranches } from '~/components/workspace/BranchSelect'
import { resolveStampedBranch } from '~/components/workspace/branchStamp'
import { BranchStatusInfo, hasPushableWork } from '~/components/workspace/BranchStatusInfo'
import { PushBranchButton } from '~/components/workspace/PushBranchButton'
import { useOrg } from '~/context/OrgContext'
import { TabType } from '~/generated/leapmux/v1/workspace_pb'
import { useDeleteBranchInspect } from '~/hooks/useDeleteBranchInspect'
import { useDialogSubmit } from '~/hooks/useDialogSubmit'
import { formatErrorMessage } from '~/lib/errors'
import { isAgentTab, isPushableTab, isTerminalTab } from '~/stores/tab.types'
import { errorText } from '~/styles/shared.css'

function countTabs(tabs: readonly Tab[]): { agents: number, terminals: number, files: number } {
  let agents = 0
  let terminals = 0
  let files = 0
  for (const t of tabs) {
    if (isAgentTab(t))
      agents++
    else if (isTerminalTab(t))
      terminals++
    else if (t.type === TabType.FILE)
      files++
  }
  return { agents, terminals, files }
}

interface DeleteBranchDialogProps {
  workerId: string
  /**
   * `git rev-parse --show-toplevel` of the branch group's working dir.
   * Same value as `Tab.gitToplevel`.
   */
  gitToplevel: string
  /**
   * Current branch on the row that opened the dialog. Carried in the
   * ref purely so the sidebar can pass through what it already knows;
   * the dialog reads its displayed branch from `inspectBranchDeletion`.
   * `null` for the sidebar's "(no branch)" group.
   */
  branchName: string | null
  /** Snapshot of tabs in the branch group at dialog-open time. */
  tabs: Tab[]
  /**
   * Close every tab in the group with WorktreeAction.REMOVE and resolve
   * with what actually happened to the worktree. Routes through the
   * parent's shared close pipeline so each tab runs the full cleanup
   * (control-store, attachments, xterm instance disposal, per-tab
   * close-failure toast, focus migration, empty-floating-window prune),
   * then folds the per-close outcomes into one summary so the dialog can
   * toast the truth (removed / still referenced elsewhere / failed /
   * couldn't confirm) instead of optimistically promising a removal.
   */
  closeWorktreeTabs: (tabs: readonly Tab[]) => Promise<WorktreeCloseSummary>

  /**
   * Notified after a non-worktree delete with the branch the working
   * directory was switched to. Parents route this into
   * `tabStore.stampBranchOnTabs` (which carries the rationale).
   * Not called for the worktree path (those tabs are being removed
   * entirely).
   */
  onBranchChanged?: (newBranch: string) => void
  onClose: () => void
}

/**
 * Maps a folded worktree-close outcome (from closeWorktreeTabs) plus
 * whether the worktree was tracked at inspect time to the info toast the
 * dialog should show, or null when it must stay silent (a FAILED close
 * already warn-toasted its own git error + path for manual cleanup).
 *
 * Precedence is ground-truth-first: a real REMOVED / STILL_REFERENCED
 * outcome reported by the worker wins over the inspect-time
 * `trackedAtInspect` snapshot, which can be stale — the worktree may have
 * been adopted (gained a DB row) between inspect and confirm. Exported so
 * the precedence can be unit-tested in isolation, without rendering.
 */
export function worktreeRemovalToast(
  outcome: WorktreeCloseSummary,
  trackedAtInspect: boolean,
): string | null {
  if (outcome.removed) {
    // A close brought the worktree's ref-count to zero and the worker
    // removed it. Ground truth, so it wins over both the stale-snapshot
    // `trackedAtInspect` check and a sibling close's partial failure
    // (which already warn-toasted its own detail).
    return 'Worktree removed'
  }
  if (outcome.failed) {
    // The close pipeline already warn-toasted the git error and the
    // worktree path for manual cleanup (toastCloseFailure); don't also
    // claim success.
    return null
  }
  if (outcome.stillReferenced) {
    // A close dropped this tab's link but the worker still counted
    // siblings — tabs in another branch group, or a now-stale snapshot —
    // so it correctly kept the worktree. Only a tracked worktree can ever
    // report STILL_REFERENCED (an untracked one degrades REMOVE to KEEP),
    // so this wins over the stale empty-`worktreeId` snapshot below: a
    // worktree adopted between inspect and confirm is tracked-and-in-use,
    // not "untracked".
    return 'Tabs closed; worktree still in use elsewhere'
  }
  if (outcome.unknown) {
    // At least one close returned no definitive outcome — its RPC was
    // rejected, there was no worker to reach, or the local close threw
    // (each already warn-toasted its own detail). The worker may or may not
    // have removed the worktree, so we can't honestly claim either "removed"
    // or "not removed" — say it couldn't be confirmed. Ranks below the
    // definitive removed/failed/still-referenced signals (which come from
    // tabs that DID get a verdict) and above the stale inspect snapshot.
    return 'Tabs closed; could not confirm worktree removal'
  }
  if (!trackedAtInspect) {
    // No DB row backed this worktree (created outside LeapMux via `git
    // worktree add`) and nothing removed it, so REMOVE degraded to KEEP
    // server-side and the dir stays on disk — say so rather than claiming
    // a removal.
    return 'Tabs closed (worktree was not tracked)'
  }
  // Tracked, but no close removed it, failed, or reported it still
  // referenced: every close degraded to KEEP because its worktree link was
  // already gone (e.g. a startup-race strand the worker's worktree GC will
  // reclaim). Nothing was removed — say so without implying another tab is
  // holding it.
  return 'Tabs closed; worktree not removed'
}

export const DeleteBranchDialog: Component<DeleteBranchDialogProps> = (props) => {
  const org = useOrg()
  // The dialog is locked to (props.workerId, props.gitToplevel) — no
  // worker selector, no directory picker, no git-mode state. Both delete
  // paths (see handleWorktreeDelete vs handleBranchDelete) drive `run` so
  // the dialog holds open under the busy overlay until the work settles:
  // the worktree path closes every tab with REMOVE and awaits the
  // worker's verdict (so its toast reflects the real outcome rather than
  // an optimistic guess); the branch path awaits DeleteBranch. `error`/
  // `setError` also back the inspect RPC's error sink (see
  // useDeleteBranchInspect's onError below), which runs while open.
  const { submitting, error, setError, run } = useDialogSubmit({ fallback: 'Delete failed' })

  // The inspect RPC fans out path-info + snapshot + branches concurrently
  // inside the worker (errgroup), so one round trip returns everything
  // the dialog needs. Post-push, `inspect.refresh()` re-issues only this
  // RPC, picking up any new `origin/<branch>` ref the push created.
  /* eslint-disable solid/reactivity -- dialog is locked to (workerId, gitToplevel) for its lifetime */
  const inspect = useDeleteBranchInspect({
    workerId: props.workerId,
    gitToplevel: props.gitToplevel,
    branchName: props.branchName,
    onError: err => setError(formatErrorMessage(err, 'Failed to inspect branch')),
  })
  /* eslint-enable solid/reactivity */
  const info = inspect.info
  const [switchTo, setSwitchTo] = createSignal('')

  // Selectable branches exclude the one being deleted. The inspect RPC
  // returns the candidate list directly (only populated server-side when
  // !isWorktree), so the dialog renders the picker as soon as the
  // single inspect call lands — no second listGitBranches round trip.
  // Partition once into local/remote for BranchSelect; the picker reads
  // these arrays directly instead of re-walking the list every render.
  const branchPartition = createMemo(() => {
    const i = info()
    if (!i)
      return { local: [], remote: [] }
    return partitionBranches(i.branches.filter(b => b.name !== i.branchName))
  })

  // props.tabs is a snapshot frozen at dialog-open time per the
  // DeleteBranchDialogProps contract, so a one-shot read is correct.
  // eslint-disable-next-line solid/reactivity
  const tabCounts = countTabs(props.tabs)
  // PushBranch + the FILE-tab-orphan guard both need the first
  // worker-pushable tab (AGENT or TERMINAL). FILE tabs carry no
  // server-side working dir, so feeding one into pushBranch's
  // loadTabGitContext path lands on `unsupported tab type`. Pin once
  // — same one-shot rationale as tabCounts.
  // eslint-disable-next-line solid/reactivity
  const pushableTab = props.tabs.find(isPushableTab)

  const isOnlyBranch = () => {
    const i = info()
    if (!i || i.isWorktree)
      return false
    const p = branchPartition()
    return p.local.length === 0 && p.remote.length === 0
  }

  const canSubmit = () => {
    const i = info()
    if (!i)
      return false
    // Gate re-clicks while either delete is in flight (both paths drive
    // `run`, so the busy overlay is up and a second confirm must no-op).
    if (submitting.loading())
      return false
    if (i.isWorktree)
      return true
    return !isOnlyBranch() && switchTo() !== ''
  }

  // Worktree removal is coupled to the tab closes — the same path the
  // last-tab-close dialog uses — rather than a dedicated worktree-removal
  // RPC. It runs through `run` so the dialog holds open under the busy
  // overlay while the closes settle, then toasts the REAL outcome instead
  // of optimistically promising a removal that may not happen.
  const handleWorktreeDelete = (i: InspectBranchDeletionResponse) => {
    void run(async () => {
      // closeWorktreeTabs hands every tab to closeTabWithAction with
      // WorktreeAction.REMOVE (local cleanup is synchronous, so the tabs
      // vanish immediately) and awaits the worker's verdict for each. The
      // worker ref-counts worktree_tabs (type-agnostic — FILE tabs
      // included) and runs `git worktree remove` + branch delete + DB
      // soft-delete once the LAST referencing tab closes, serializing
      // concurrent closes per worktree so there is no double-remove.
      const outcome = await props.closeWorktreeTabs(props.tabs)
      props.onClose()
      // Toast the REAL outcome. worktreeRemovalToast owns the precedence
      // (ground truth over the stale inspect-time worktreeId snapshot);
      // null means stay silent because a FAILED close already warned.
      const message = worktreeRemovalToast(outcome, Boolean(i.worktreeId))
      if (message)
        showInfoToast(message)
    })
  }

  // Non-worktree branch delete keeps the tabs running on the switched-to
  // branch, so there's nothing to close; the user is mid-decision (which
  // branch to switch to). It runs through `run` so the dialog holds open
  // under the busy overlay until DeleteBranch (checkout switch-to + branch
  // -D) completes; a failure surfaces inline and the user can fix the
  // switch-to target or retry without losing the dialog.
  const handleBranchDelete = (i: InspectBranchDeletionResponse) => {
    void run(async () => {
      const target = switchTo()
      await workerRpc.deleteBranch(props.workerId, {
        orgId: org.orgId(),
        workerId: props.workerId,
        path: props.gitToplevel,
        branchToDelete: i.branchName,
        switchToBranch: target,
      })
      // The delete succeeded on the worker. Surface success and close
      // BEFORE the stamp so a throw from onBranchChanged can't propagate
      // into `run`'s catch and masquerade as a "Delete failed" — leaving
      // the user staring at a failure banner for an op that worked.
      showInfoToast('Branch deleted')
      props.onClose()
      // deleteBranchInDir routes through checkoutBranchInDir, which resolves
      // a remote ref like 'origin/foo' to the local branch 'foo' before
      // deleting. Stamp the local name so the sidebar label matches HEAD.
      // ChangeBranchDialog stamps via the same helper. Isolated because
      // the stamp is cosmetic (sidebar label) and must not undo success.
      try {
        props.onBranchChanged?.(resolveStampedBranch(target, i.branches))
      }
      catch (err) {
        showWarnToast('Branch deleted, but failed to update the sidebar label', err)
      }
    })
  }

  const handleDelete = () => {
    const i = info()
    if (!i)
      return
    if (i.isWorktree)
      handleWorktreeDelete(i)
    else
      handleBranchDelete(i)
  }

  return (
    <WorkerDialogShell
      title="Delete branch"
      // Drives the busy overlay for both delete paths while their `run`
      // is in flight (worktree: closing tabs + awaiting removal; branch:
      // DeleteBranch). The inspect RPC's error sink also surfaces here.
      submitting={submitting.loading()}
      error={error()}
      onClose={props.onClose}
      compact
      footer={(
        <>
          <button type="button" class="outline" onClick={() => props.onClose()}>
            Cancel
          </button>
          <Show when={hasPushableWork(info()?.gitState) && pushableTab}>
            {/* Push needs a tab whose worker-side getTabWorkingDir can
                resolve a working directory — i.e. AGENT or TERMINAL.
                FILE tabs slip past a naive `props.tabs[0]` and surface
                as `unsupported tab type` from the worker. The Show
                gate also covers the all-FILE / empty array edge by
                falling out when `pushableTab` is undefined. */}
            {pt => (
              <PushBranchButton
                workerId={props.workerId}
                tab={{ type: pt().type, id: pt().id }}
                gitState={info()?.gitState}
                onPushed={inspect.refresh}
                disabled={submitting.loading()}
              />
            )}
          </Show>
          <ConfirmButton data-variant="danger" disabled={!canSubmit()} onClick={handleDelete}>
            Delete branch
          </ConfirmButton>
        </>
      )}
    >
      <Show when={info()}>
        {i => (
          <>
            <BranchStatusInfo
              branch={i()}
              affectedTabs={{
                agents: isOnlyBranch() ? 0 : tabCounts.agents,
                terminals: isOnlyBranch() ? 0 : tabCounts.terminals,
                files: isOnlyBranch() ? 0 : tabCounts.files,
                willStop: i().isWorktree,
              }}
            />
            {/* A push-then-refresh keeps `info()` non-null but kicks the
                inspect RPC again. Without this indicator the dialog
                would render the stale pre-push state until the refresh
                lands, which contradicts the BranchStatusInfo counts the
                user just acted on. */}
            <Show when={inspect.loading()}>
              <div class={labelRow} data-testid="delete-branch-refresh-indicator">
                <Spinner />
                Refreshing branch state
              </div>
            </Show>
            <Show when={!i().isWorktree}>
              <Show
                when={!isOnlyBranch()}
                fallback={(
                  <div class={errorText}>
                    Cannot delete the only branch. Create another branch first.
                  </div>
                )}
              >
                <div>
                  <div class={labelRow}>Switch this working directory to:</div>
                  <BranchSelect
                    value={switchTo()}
                    onChange={setSwitchTo}
                    local={branchPartition().local}
                    remote={branchPartition().remote}
                    showPrompt
                  />
                </div>
              </Show>
            </Show>
          </>
        )}
      </Show>
      <Show when={!info() && !error()}>
        <div class={labelRow}>
          <Spinner />
          Inspecting branch state
        </div>
      </Show>
    </WorkerDialogShell>
  )
}
