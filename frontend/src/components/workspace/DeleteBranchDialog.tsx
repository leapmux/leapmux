import type { Component } from 'solid-js'
import type { Tab } from '~/stores/tab.types'
import { createMemo, createSignal, Show } from 'solid-js'
import * as workerRpc from '~/api/workerRpc'
import { ConfirmButton } from '~/components/common/ConfirmButton'
import { labelRow } from '~/components/common/Dialog.css'
import { Spinner } from '~/components/common/Spinner'
import { showInfoToast } from '~/components/common/Toast'
import { WorkerDialogShell } from '~/components/shell/WorkerDialogShell'
import { BranchSelect, partitionBranches } from '~/components/workspace/BranchSelect'
import { resolveStampedBranch } from '~/components/workspace/branchStamp'
import { BranchStatusInfo, hasPushableWork } from '~/components/workspace/BranchStatusInfo'
import { PushBranchButton } from '~/components/workspace/PushBranchButton'
import { useOrg } from '~/context/OrgContext'
import { WorktreeAction } from '~/generated/leapmux/v1/common_pb'
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
   * Close one tab and its worktree, routing through the parent's
   * shared tab-close helper so the close path runs the full cleanup
   * (control-store, attachments, xterm instance disposal, close-failure
   * toast, focus migration, empty-floating-window prune) the same way
   * a normal tab-close does.
   */
  closeTab: (tab: Tab, worktreeAction: WorktreeAction) => void
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

export const DeleteBranchDialog: Component<DeleteBranchDialogProps> = (props) => {
  const org = useOrg()
  // Dialog is locked to (props.workerId, props.gitToplevel) — no worker
  // selector, no directory picker, no git-mode state — so the
  // useWorkerDialog scaffolding would be dead weight. The submit
  // primitive on its own gives us the spinner + error sink.
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
    if (submitting.loading())
      return false
    if (i.isWorktree)
      return true
    return !isOnlyBranch() && switchTo() !== ''
  }

  const handleDelete = async () => {
    const i = info()
    if (!i)
      return
    await run(async () => {
      if (i.isWorktree) {
        // Worktree removal is driven by ForceRemoveWorktree against the
        // worktree's DB row id, which the inspect RPC returns alongside
        // is_worktree. This decouples the deletion from tab existence:
        // a branch group with only FILE tabs (or no tabs at all once
        // they've all been closed) used to be a silent no-op because
        // the FILE close path doesn't ref-count the worktree on the
        // worker side.
        if (i.worktreeId) {
          // Await ForceRemoveWorktree BEFORE closing the tabs. The
          // close calls are fire-and-forget; closing them first and
          // then catching a worktree-remove failure leaves the user
          // staring at a "Delete failed" banner above an already-
          // empty branch group with no path to retry. Order matters
          // even though closing is for UI cleanup only (control-
          // store clear for agents, xterm instance disposal for
          // terminals, file-path revoke for FILE).
          await workerRpc.forceRemoveWorktree(props.workerId, {
            worktreeId: i.worktreeId,
          })
          // Tabs are closed with KEEP — the backend already removed
          // the worktree, so the per-tab last-close pipeline must
          // not race ForceRemoveWorktree by trying again.
          for (const tab of props.tabs)
            props.closeTab(tab, WorktreeAction.KEEP)
          showInfoToast('Worktree removed')
        }
        else {
          // Untracked worktree edge case: worktree dir exists on disk
          // but no DB row is registered (commonly happens when the
          // user created the worktree from a terminal via `git
          // worktree add` before opening any LeapMux tab inside it).
          // The worker's proto contract documents this exact case:
          // when GetWorktreeByPath returns ErrNoRows, worktree_id is
          // returned empty and the dialog "falls back to closing tabs
          // through their own pipeline." Hard-failing here used to
          // strand the user with no UI path to clean up the worktree
          // the worker can clearly see.
          //
          // Fall back to closing tabs with REMOVE: each tab's close
          // pipeline tries to ref-count the worktree down to zero
          // through its normal pipeline. The on-disk worktree dir
          // can't be removed without a DB row backing it, but at
          // least the user's tabs go away and the branch group
          // collapses in the sidebar.
          for (const tab of props.tabs)
            props.closeTab(tab, WorktreeAction.REMOVE)
          showInfoToast('Tabs closed (worktree was not tracked)')
        }
      }
      else {
        const target = switchTo()
        await workerRpc.deleteBranch(props.workerId, {
          orgId: org.orgId(),
          workerId: props.workerId,
          path: props.gitToplevel,
          branchToDelete: i.branchName,
          switchToBranch: target,
        })
        // The worker's deleteBranchInDir routes through checkoutBranchInDir,
        // which resolves a remote ref like 'origin/foo' to the local
        // branch 'foo' before deleting. Stamp the local name so the
        // sidebar label matches HEAD. ChangeBranchDialog uses the same
        // helper for the symmetric reason.
        props.onBranchChanged?.(resolveStampedBranch(target, i.branches))
        showInfoToast('Branch deleted')
      }
      props.onClose()
    })
  }

  return (
    <WorkerDialogShell
      title="Delete branch"
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
