import type { Component } from 'solid-js'
import type { InspectBranchDeletionResponse } from '~/generated/leapmux/v1/git_pb'
import type { Tab } from '~/stores/tab.types'
import { createMemo, createSignal, Show } from 'solid-js'
import * as workerRpc from '~/api/workerRpc'
import { ConfirmButton } from '~/components/common/ConfirmButton'
import { labelRow } from '~/components/common/Dialog.css'
import { Spinner } from '~/components/common/Spinner'
import { showInfoToast, showWarnToast } from '~/components/common/Toast'
import { Tooltip } from '~/components/common/Tooltip'
import { WorkerDialogShell } from '~/components/shell/WorkerDialogShell'
import { BranchSelect, partitionBranches } from '~/components/workspace/BranchSelect'
import { resolveStampedBranch } from '~/components/workspace/branchStamp'
import { BranchStatusInfo, hasPushableWork } from '~/components/workspace/BranchStatusInfo'
import { PushBranchButton } from '~/components/workspace/PushBranchButton'
import { WorktreeAction } from '~/generated/leapmux/v1/common_pb'
import { TabType } from '~/generated/leapmux/v1/workspace_pb'
import { useDeleteBranchInspect } from '~/hooks/useDeleteBranchInspect'
import { useDialogSubmit } from '~/hooks/useDialogSubmit'
import { formatErrorMessage } from '~/lib/errors'
import { createLogger } from '~/lib/logger'
import { isAgentTab, isTerminalTab } from '~/stores/tab.types'
import { errorText, warningText } from '~/styles/shared.css'

const log = createLogger('DeleteBranchDialog')

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
   * Close every tab in the group with `action`, and report what actually
   * happened to the worktree when the closes settle. Routes through the
   * parent's shared close pipeline so each tab runs the full cleanup
   * (control-store, attachments, xterm instance disposal, per-tab
   * close-failure toast, focus migration, empty-floating-window prune).
   *
   * It returns nothing, and the dialog does not await it. The work outlives
   * this dialog by seconds, so the report belongs to the pipeline that owns
   * the work and not to a closure rooted in a dismissed component. See
   * `closeWorktreeTabsAndReport` in useTabOperations.
   *
   * `trackedAtInspect` is the dialog's inspect-time snapshot of whether
   * LeapMux tracked the worktree; the reporter ranks it below every
   * definitive worker outcome.
   */
  closeWorktreeTabs: (tabs: readonly Tab[], action: WorktreeAction, trackedAtInspect: boolean) => void

  /**
   * Notified after a non-worktree delete with the branch the working
   * directory was switched to. Parents route this into AppShell's
   * `onBranchChanged` handler, which stamps every tab in the same repo across
   * every workspace (it carries the rationale).
   * Not called for the worktree path (those tabs are being removed
   * entirely).
   */
  onBranchChanged?: (newBranch: string) => void
  onClose: () => void
}

export const DeleteBranchDialog: Component<DeleteBranchDialogProps> = (props) => {
  // The dialog is locked to (props.workerId, props.gitToplevel) — no
  // worker selector, no directory picker, no git-mode state. Both delete
  // paths (see handleWorktreeDelete vs handleBranchDelete) drive `run`.
  // Each one holds the dialog open under the busy overlay for as long as
  // it can still act on what it learns:
  //
  //   - the worktree path holds for its PREFLIGHT only. Then it hands the
  //     tab closes off and dismisses. The removal that follows is long,
  //     and it asks the user nothing.
  //   - the branch path holds for the whole DeleteBranch RPC. It fails
  //     when a dirty working tree stops the checkout, which no preflight
  //     states in advance. Only this dialog can report that failure, and
  //     the user corrects the switch-to target and tries again here.
  //
  // `error`/`setError` also back the inspect RPC's error sink (see
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
  // The dir the group shares, taken from any tab in it. `workingDir` is
  // client-side metadata on the joined tab, so this needs no worker row -- which
  // is what retires the old anchor-tab dance: an anchor's worker-side row could
  // be absent (a FILE tab's lives in `worker_file_tabs`, which a peer's close
  // hard-deletes) and the push failed with "file tab path not found" even though
  // a healthy sibling sat next to it. Every tab in a branch group shares the
  // dir by construction, so there is nothing to prefer between them.
  // Pin once — same one-shot rationale as tabCounts.
  // eslint-disable-next-line solid/reactivity
  const pushWorkingDir = props.tabs.find(t => t.workingDir)?.workingDir ?? ''

  const isOnlyBranch = () => {
    const i = info()
    if (!i || i.isWorktree)
      return false
    const p = branchPartition()
    return p.local.length === 0 && p.remote.length === 0
  }

  // Why git refuses the worktree removal, from the OPEN-time inspect. The
  // Delete button reads it, so the user learns the refusal before arming a
  // destructive two-click confirm rather than after firing it. The worker
  // populates it only for a worktree, and this restates that gate so a reason
  // arriving on any other response cannot render a warning about a removal
  // this dialog never offers.
  const removalBlockedReason = createMemo(() => {
    const i = info()
    return i?.isWorktree ? i.worktreeRemovalBlockedReason : ''
  })

  const canSubmit = () => {
    const i = info()
    if (!i)
      return false
    // Gate re-clicks while either delete is in flight (both paths drive
    // `run`, so the busy overlay is up and a second confirm must no-op).
    if (submitting.loading())
      return false
    if (i.isWorktree)
      return !removalBlockedReason()
    return !isOnlyBranch() && switchTo() !== ''
  }

  // The escape hatch for a refused removal. The tabs are still closable — only
  // the removal is refused — so this offers exactly what the last-tab dialog's
  // "Close anyway" offers: close the group and leave the worktree on disk.
  // Without it the dialog is a dead end, and the user closes the group one tab
  // at a time to reach the same state.
  //
  // The `trackedAtInspect` argument is inert for a KEEP: the reporter reads it
  // only to rank an inspect-time snapshot against a REMOVAL outcome, and a KEEP
  // asks for no removal.
  const handleCloseTabsKeepingWorktree = () => {
    props.closeWorktreeTabs(props.tabs, WorktreeAction.KEEP, false)
    props.onClose()
  }

  // Worktree removal is coupled to the tab closes — the same path the
  // last-tab-close dialog uses — rather than a dedicated worktree-removal
  // RPC. The dialog PREFLIGHTS the removal, then hands the closes off and
  // dismisses itself without awaiting them.
  //
  // It does not await, because the work behind those closes is long and
  // asks the user nothing. Each agent close stops a subprocess, which
  // waits out a 3-second grace before it kills the process tree. Then
  // `git worktree remove` deletes the whole directory, which takes
  // seconds on a tree that holds node_modules or target. An await held
  // the dialog under its busy overlay for that whole time, and a slow
  // delete then looked the same as a stopped one.
  //
  // The preflight is the cost of that. A dialog that already closed
  // cannot render a refusal, so the refusals git states up front (a
  // locked worktree, a path git removes never) are raised HERE, while the
  // dialog is still open and the tabs are still alive. Everything the
  // preflight cannot state still arrives as the outcome toast below.
  const handleWorktreeDelete = (i: InspectBranchDeletionResponse) => {
    void run(async () => {
      // Refuse before anything is destroyed. A blocked reason is a normal,
      // predicted outcome, so it sets the banner and returns rather than
      // travels as an exception -- the reader can tell it apart from a
      // transport failure at the site.
      //
      // A REJECTED preflight is not a refusal. The worker returns an error
      // when it cannot answer, and it fails open on exactly that error
      // (see worktreeRemovalBlockedReason); this surface must agree, or one
      // worker state refuses a delete on one surface and allows it on the
      // other. The close then reports the real outcome, which is what it
      // did before the preflight existed.
      let blockedReason = ''
      try {
        const preflight = await workerRpc.inspectWorktreeRemoval(props.workerId, {
          workerId: props.workerId,
          path: props.gitToplevel,
        })
        blockedReason = preflight.blockedReason
      }
      catch (err) {
        log.warn('worktree removal preflight failed; handing the closes off unqualified', err)
      }
      if (blockedReason) {
        setError(blockedReason)
        return
      }

      // closeWorktreeTabs hands every tab to closeTabWithAction with
      // WorktreeAction.REMOVE (local cleanup is synchronous, so the tabs
      // vanish immediately) and reports the worker's verdict when it lands.
      // The worker ref-counts worktree_tabs (type-agnostic — FILE tabs
      // included) and runs `git worktree remove` + branch delete + DB
      // soft-delete once the LAST referencing tab closes, serializing
      // concurrent closes per worktree so there is no double-remove.
      //
      // `i.worktreeId` is read HERE, before the close, because it is the
      // inspect-time snapshot the reporter ranks below the worker's own
      // outcome.
      props.closeWorktreeTabs(props.tabs, WorktreeAction.REMOVE, Boolean(i.worktreeId))
      props.onClose()
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
        workerId: props.workerId,
        path: props.gitToplevel,
        branchToDelete: i.branchName,
        switchToBranch: target,
      })
      // The delete succeeded on the worker. Surface success first, so the
      // user sees it even if the stamp below warns.
      showInfoToast('Branch deleted')
      // deleteBranchInDir routes through checkoutBranchInDir, which resolves
      // a remote ref like 'origin/foo' to the local branch 'foo' before
      // deleting. Stamp the local name so the sidebar label matches HEAD.
      // ChangeBranchDialog stamps via the same helper. The try/catch
      // isolates the stamp, because the stamp is cosmetic (the sidebar
      // label) and must not undo the success. The try/catch — NOT the call
      // order — keeps a throw here out of `run`'s catch. `run`'s catch
      // shows a "Delete failed" banner, which is incorrect for a delete
      // that worked.
      //
      // `props.onClose()` runs LAST in this handler: onClose disposes the
      // subtree that owns this dialog, so anything after it runs against a
      // disposed owner. AppShellDialogs renders this dialog under a keyed
      // <Show>, which hands its children the payload itself, so its
      // callbacks hold no accessor to go stale. This order keeps every
      // other parent safe also. An earlier close-first order swallowed the
      // stamp, and the sidebar kept the deleted branch's label until a page
      // reload.
      //
      // handleWorktreeDelete deliberately does NOT follow it: its outcome
      // toast lands after the close, because the work it reports on outlives
      // the dialog. That callback therefore reads plain values only.
      try {
        props.onBranchChanged?.(resolveStampedBranch(target, i.branches))
      }
      catch (err) {
        showWarnToast('Branch deleted, but failed to update the sidebar label', err)
      }
      props.onClose()
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
      // is in flight (worktree: the removal preflight only; branch: the
      // whole DeleteBranch RPC). The inspect RPC's error sink also
      // surfaces here.
      submitting={submitting.loading()}
      error={error()}
      onClose={props.onClose}
      compact
      footer={(
        <>
          {/* `disabled` while submitting, to match DialogFormFooter's own
              Cancel. The Dialog `busy` flag gates only Escape, the backdrop
              click, and the X button; it never reaches a custom footer
              button, which calls `props.onClose()` directly. Without this
              gate the user can dismiss the dialog while DeleteBranch is in
              flight, and a failure then has nowhere to render. */}
          <button type="button" class="outline" disabled={submitting.loading()} onClick={() => props.onClose()}>
            Cancel
          </button>
          {/* The `pushWorkingDir` gate covers the edge where no tab in the
              group has a dir yet — a branch row with nothing to push from. */}
          <Show when={hasPushableWork(info()?.gitState) && pushWorkingDir}>
            <PushBranchButton
              workerId={props.workerId}
              workingDir={pushWorkingDir}
              gitState={info()?.gitState}
              onPushed={inspect.refresh}
              disabled={submitting.loading()}
            />
          </Show>
          {/* The escape hatch for a refused removal, and the counterpart of
              the last-tab dialog's "Close anyway": the tabs are still
              closable, only the removal is refused. Without it a refusal
              leaves Cancel as the dialog's one enabled action. */}
          <Show when={removalBlockedReason()}>
            <ConfirmButton
              data-variant="danger"
              disabled={submitting.loading()}
              onClick={handleCloseTabsKeepingWorktree}
            >
              Close tabs, keep worktree
            </ConfirmButton>
          </Show>
          {/* The spinner-in-submit pattern of DialogFormFooter, which this
              custom footer cannot use. The label states what is actually
              in flight, and the two paths wait for different things: the
              worktree path checks whether git removes the worktree, the
              branch path runs the delete itself.

              The blocked reason goes through <Tooltip>, which works on a
              disabled control and leaves the button its own name. On `title`
              the reason BECOMES that name, and this button's name is STATE
              ("Confirm?" once armed) -- so the reason replaced the one thing
              the name had to carry, and a role+name lookup for "Delete
              branch" stopped matching. The same text also renders in the body
              above, for anybody who never hovers. */}
          <Tooltip text={removalBlockedReason() || undefined}>
            <ConfirmButton
              data-variant="danger"
              disabled={!canSubmit()}
              onClick={handleDelete}
            >
              <Show when={submitting.loading()} fallback="Delete branch">
                <Spinner />
                {info()?.isWorktree ? 'Checking...' : 'Deleting...'}
              </Show>
            </ConfirmButton>
          </Tooltip>
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
            {/* Why the Delete button is unavailable. Visible text, not the
                button's `title` alone, because a greyed-out destructive
                option with no stated reason looks like a defect. */}
            <Show when={removalBlockedReason()}>
              <div class={warningText}>{removalBlockedReason()}</div>
            </Show>
            {/* The worker sets error_hint when a probe it needed did not
                answer. An empty blocked reason is also what a clean worktree
                sends, so without this the user cannot tell a removal git
                accepts from one nobody checked. */}
            <Show when={i().errorHint}>
              <div class={warningText}>{i().errorHint}</div>
            </Show>
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
