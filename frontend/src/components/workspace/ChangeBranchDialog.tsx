import type { Component } from 'solid-js'
import type { AgentInfo, AgentProvider } from '~/generated/proto/leapmux/v1/agent_pb'
import type { ChangeBranchMode } from '~/hooks/useGitModeState'
import { createEffect, createSignal, Match, on, Show, Switch } from 'solid-js'
import * as workerRpc from '~/api/workerRpc'
import { openAgentRequestOptions } from '~/components/chat/providers/registry'
import { labelRow } from '~/components/common/Dialog.css'
import { PillGroup } from '~/components/common/PillGroup'
import { WorkingTreeRows } from '~/components/common/WorkingTree'
import { AgentProviderSelector } from '~/components/shell/AgentProviderSelector'
import { BlockedReasonNotice } from '~/components/shell/BlockedReasonNotice'
import { isChangeBranchSubmitDisabled } from '~/components/shell/dialogValidation'
import { GitOptions } from '~/components/shell/GitOptions'
import { GitOptionsLoader } from '~/components/shell/GitOptionsLoader'
import { ShellSelector } from '~/components/shell/ShellSelector'
import { TitleInput } from '~/components/shell/TitleInput'
import { DialogFormFooter, WorkerDialogShell } from '~/components/shell/WorkerDialogShell'
import { resolveStampedBranch } from '~/components/workspace/branchStamp'
import { TabType } from '~/generated/proto/leapmux/v1/workspace_pb'
import { createTitleState } from '~/hooks/createTitleState'
import { createWorkerDialogContext } from '~/hooks/createWorkerDialogContext'
import { useAgentProviderSelection } from '~/hooks/useAgentProviderSelection'
import { useAvailableShells } from '~/hooks/useAvailableShells'
import { useChangeBranchInspect } from '~/hooks/useChangeBranchInspect'
import { useDialogSubmit } from '~/hooks/useDialogSubmit'
import { CHANGE_BRANCH_MODES, changeBranchInitialIntent, fieldsForCreateWorktree, GitMode, isChangeBranchMode, useGitModeState } from '~/hooks/useGitModeState'
import { formatErrorMessage } from '~/lib/errors'
import { createLogger } from '~/lib/logger'
import { flavorFromOs } from '~/lib/paths'
import { randomAgentTitle, randomTerminalTitle } from '~/lib/tabTitles'
import { DEFAULT_TERMINAL_COLS, DEFAULT_TERMINAL_ROWS } from '~/lib/terminal'
import { workerInfoStore } from '~/stores/workerInfo.store'
import { errorText, warningText } from '~/styles/shared.css'

type WorktreeTabType = TabType.AGENT | TabType.TERMINAL

const log = createLogger('ChangeBranchDialog')

interface ChangeBranchDialogProps {
  workerId: string
  /**
   * `git rev-parse --show-toplevel` of the branch group's working dir.
   * Same value as `Tab.gitToplevel` — main-repo root for branch tabs,
   * worktree root for linked-worktree tabs.
   */
  gitToplevel: string
  /**
   * Current branch label on the row that opened the dialog. Threaded so
   * the dialog can seed `currentBranch` synchronously instead of waiting
   * for the post-mount probe to land. `null` for rows with no current
   * branch.
   */
  branchName: string | null
  /**
   * True iff `gitToplevel` resolves to a linked worktree. Threaded so
   * `useChangeBranchInspect` can seed `isWorktreeRoot` / `isRepoRoot`
   * correctly — without this, a worktree-opened dialog briefly paints a
   * main-repo shape and downstream GitOptions memos (e.g. suggested
   * worktree path) compute against the wrong fields until the inspect
   * RPC lands.
   */
  isWorktree: boolean
  /**
   * The mode the dialog opens on. The branch context menu offers one item per
   * mode, so the item the user picked decides which radio is already selected
   * when the dialog paints.
   */
  initialMode: ChangeBranchMode
  availableProviders?: AgentProvider[]
  onRefreshProviders?: () => void
  /**
   * Notified after a successful in-place checkout or branch creation
   * with the local branch the working directory is now on. Parents
   * route this into AppShell's `onBranchChanged` handler, which stamps the
   * new label onto every tab in the same (workerId, gitToplevel) repo across
   * every workspace (it carries the rationale for why direct stamping is
   * needed rather than waiting for the next git-status poll).
   */
  onBranchChanged?: (newBranch: string) => void
  /**
   * When this returns a string in CreateWorktree mode, no tab can be
   * placed for the new worktree's agent/terminal (no workspace tree, or
   * an archived workspace): submit is disabled and the string is shown
   * as the reason. Guards the worker RPC — creating the resource first
   * and refusing placement second would orphan it. The branch-only modes
   * open no tab, so the reason never applies to them.
   */
  blockedReason?: () => string | undefined
  onAgentCreated?: (agent: AgentInfo) => void
  onTerminalCreated?: (terminalId: string, workerId: string, workingDir: string, title: string) => void
  onClose: () => void
}

export const ChangeBranchDialog: Component<ChangeBranchDialogProps> = (props) => {
  /* eslint-disable solid/reactivity -- initial-mount snapshot; the dialog is opened against a fixed (workerId, gitToplevel) and stays on them */
  const { submitting, error, setError, formHandler } = useDialogSubmit()
  const worker = createWorkerDialogContext({
    singleWorkerId: props.workerId,
    defaultWorkingDir: props.gitToplevel,
    onError: setError,
  })
  // The worker's path flavor for the header row's tilde path. Undefined while
  // the OS is unknown, so `tildify` sniffs it from the path rather than being
  // forced to posix — `flavorFromOs(undefined)` answers 'posix'.
  const workerFlavor = () => {
    const os = workerInfoStore.getOs(worker.workerId())
    return os ? flavorFromOs(os) : undefined
  }
  // The dialog renders SwitchBranch / CreateBranch / CreateWorktree
  // (Current is intentionally excluded). Seed the parent intent so
  // GitOptions paints the caller's mode selected on first render.
  const gitMode = useGitModeState(changeBranchInitialIntent(props.initialMode))
  // One bundle RPC at dialog open returns path-info + branches + dirty
  // in a single round trip. The resulting pathInfo plugs into GitOptions
  // exactly like useGitPathInfo would; the branches list rides through
  // GitOptions's `preloadedBranches` prop so GitOptions skips its own
  // ListGitBranches fetcher.
  const inspect = useChangeBranchInspect({
    workerId: props.workerId,
    gitToplevel: props.gitToplevel,
    branchName: props.branchName,
    isWorktree: props.isWorktree,
    onError: err => setError(formatErrorMessage(err, 'Failed to inspect branch')),
  })
  const pathInfo = inspect.pathInfo
  /* eslint-enable solid/reactivity */

  const { agentProvider, setAgentProvider, recordProviderUse, noProviders } = useAgentProviderSelection(
    () => props.availableProviders,
  )

  const [worktreeTabType, setWorktreeTabType] = createSignal<WorktreeTabType>(TabType.AGENT)

  // The generated title's prefix has to match the tab type the Open-as toggle
  // picks, so the generator reads that toggle.
  const title = createTitleState(() => (
    worktreeTabType() === TabType.AGENT ? randomAgentTitle() : randomTerminalTitle()
  ))
  // Flipping the toggle re-rolls the title, or the new tab carries the other
  // kind's prefix ("Agent Gabe" on a terminal). `regenerateIfPristine` is what
  // keeps that from discarding a name the user typed. `defer` skips the run on
  // mount, which would only re-roll the title the constructor just generated.
  createEffect(on(worktreeTabType, () => title.regenerateIfPristine(), { defer: true }))

  const shellState = useAvailableShells(
    () => {
      if (gitMode.gitMode() === GitMode.CreateWorktree && worktreeTabType() === TabType.TERMINAL) {
        return { workerId: props.workerId }
      }
      return null
    },
    err => log.warn('Failed to list shells', err),
  )
  const { shell } = shellState

  // The guard reason applies only to the one mode whose submit opens a
  // worker-side resource that placement could orphan; branch-only modes
  // change no tabs. One accessor so the submit gate and the notice read
  // the same value.
  const worktreeBlockedReason = () =>
    gitMode.currentIntent().mode === GitMode.CreateWorktree ? props.blockedReason?.() : undefined

  const submitDisabled = () => isChangeBranchSubmitDisabled({
    submitting: submitting.loading(),
    blockedReason: worktreeBlockedReason(),
    // SwitchBranch intent now carries `checkoutBranchError` set by
    // GitOptions when the destination resolves to the current branch
    // (the path-info probe's currentBranch is the source of truth, and
    // it lives in GitOptions where the branches-list lookup also lives).
    // No extra plumbing needed here.
    git: gitMode.currentIntent(),
    workerId: props.workerId,
    workingDir: props.gitToplevel,
    worktreeTabType: worktreeTabType(),
    // Only CreateWorktree RENDERS the Title field, and only its submit sends a
    // title, so the other modes contribute none: an emptied title must not
    // block a plain branch switch. Decided HERE, where the mode already decides
    // what to render, which is the rule `blockedReason` above follows too.
    titleError: gitMode.gitMode() === GitMode.CreateWorktree ? title.error() : null,
    noProviders: noProviders(),
    shell: shell(),
  })

  // Parent callbacks (onBranchChanged / onAgentCreated /
  // onTerminalCreated) run AFTER the RPC has already mutated worker
  // state. A throw inside them must NOT be reported as an RPC failure:
  // useDialogSubmit's catch sets the error banner from any throw
  // inside dispatchMode, which on a successful checkout would lie to
  // the user (HEAD moved, dialog says "Operation failed"). Funnel
  // every parent-callback site through this helper so each call site
  // collapses to one line AND a future fifth callback added to props
  // can't forget the try/catch (the WHAT-comment per the callback
  // name is the audit trail).
  const safeCallback = <T,>(name: string, fn: ((arg: T) => void) | undefined, arg: T): void => {
    if (!fn)
      return
    try {
      fn(arg)
    }
    catch (callbackErr) {
      log.warn(`${name} callback threw`, callbackErr)
    }
  }

  const dispatchMode = async (): Promise<void> => {
    const baseArgs = {
      workerId: props.workerId,
      path: props.gitToplevel,
    }
    const intent = gitMode.currentIntent()
    // The dialog only enables Switch/Create/CreateWorktree. Narrow once
    // here so the switch below is statically exhaustive over the three
    // supported variants without a runtime default.
    if (!isChangeBranchMode(intent.mode))
      throw new Error(`Unexpected git mode: ${GitMode[intent.mode] ?? 'unknown'}`)
    switch (intent.mode) {
      case GitMode.SwitchBranch: {
        const target = intent.checkoutBranch
        await workerRpc.checkoutBranch(props.workerId, { ...baseArgs, branch: target })
        // checkoutBranchInDir on the worker resolves a remote ref like
        // "origin/foo" to the local branch "foo" before/while creating
        // it; the sidebar should reflect the local name. Look up the
        // selected entry's isRemote flag — a local branch whose name
        // contains `/` (e.g. `feature/auth`) must NOT have its prefix
        // stripped, or the sidebar stamps the wrong label.
        const stamped = resolveStampedBranch(target, inspect.branches())
        safeCallback('onBranchChanged', props.onBranchChanged, stamped)
        return
      }
      case GitMode.CreateBranch: {
        const newBranch = intent.createBranch
        await workerRpc.createBranch(props.workerId, {
          ...baseArgs,
          newBranch,
          baseBranch: intent.createBranchBase,
        })
        safeCallback('onBranchChanged', props.onBranchChanged, newBranch)
        return
      }
      case GitMode.CreateWorktree: {
        // The narrowed intent already proves we're in CreateWorktree, so
        // project its fields directly instead of routing through
        // `toGitFields()` (which re-derives the mode from `currentIntent`).
        // No workspace id: neither OpenAgentRequest nor OpenTerminalRequest has
        // a field for one -- a Worker stores no workspace, and which workspace
        // the tab lands in is decided by the CRDT tile the CLIENT places it on.
        // The key used to be spread in here and was dropped on the wire, which
        // read as if the Worker filed the tab for a non-active workspace.
        const worktreeArgs = {
          workerId: props.workerId,
          workingDir: props.gitToplevel,
          ...fieldsForCreateWorktree(intent),
        }
        if (worktreeTabType() === TabType.AGENT) {
          const provider = agentProvider()
          if (provider === undefined)
            throw new Error('No agent provider available')
          const resp = await workerRpc.openAgent(props.workerId, {
            ...worktreeArgs,
            agentProvider: provider,
            // The CLEANED title, for the reason createTitleState.cleaned states.
            title: title.cleaned(),
            // And whether the user kept the suggestion, for the reason
            // createTitleState.isPristine states.
            titleAutoGenerated: title.isPristine(),
            ...openAgentRequestOptions(provider),
          })
          if (resp.agent) {
            recordProviderUse(provider)
            safeCallback('onAgentCreated', props.onAgentCreated, resp.agent)
          }
          return
        }
        const resp = await workerRpc.openTerminal(props.workerId, {
          ...worktreeArgs,
          cols: DEFAULT_TERMINAL_COLS,
          rows: DEFAULT_TERMINAL_ROWS,
          shell: shell(),
          title: title.cleaned(),
        })
        // onTerminalCreated takes a 4-tuple — bundle as an object so
        // safeCallback's single-arg shape stays consistent across
        // every call site. Capture the prop into a local before the
        // ternary so the solid/reactivity lint doesn't flag the
        // prop read as untracked: the dispatch is already running
        // outside a reactive scope and a stale handler would just
        // be a no-op, but the local makes intent explicit.
        const onTerminalCreated = props.onTerminalCreated
        safeCallback(
          'onTerminalCreated',
          onTerminalCreated
            ? args => onTerminalCreated(args.id, args.workerId, args.workingDir, args.title)
            : undefined,
          { id: resp.terminalId, workerId: props.workerId, workingDir: props.gitToplevel, title: resp.title },
        )
      }
    }
  }

  const handleSubmit = formHandler(submitDisabled, async () => {
    await dispatchMode()
    props.onClose()
  })

  return (
    <WorkerDialogShell
      title="Change branch"
      submitting={submitting.loading()}
      error={error()}
      onSubmit={handleSubmit}
      onClose={props.onClose}
      compact
      footer={(
        <DialogFormFooter
          submitting={submitting.loading()}
          submitDisabled={submitDisabled()}
          submitLabel="Apply"
          submittingLabel="Applying..."
          onClose={props.onClose}
        />
      )}
    >
      <BlockedReasonNotice reason={worktreeBlockedReason()} />
      {/* WHICH checkout this dialog acts on. The mode block below states the
          current branch for the switch picker alone; nothing stated the kind
          or the directory, so a user with the same branch name in a worktree
          and in the main repo could not tell the two dialogs apart.
          `useChangeBranchInspect` seeds both fields from the row that opened
          the dialog and replaces them when the inspect RPC lands, so this row
          is correct from the first paint -- and it KEEPS the seed when that
          RPC fails, because a failed call disproves neither fact. */}
      <WorkingTreeRows
        isWorktree={pathInfo.info().isWorktreeRoot}
        name={pathInfo.info().currentBranch}
        directory={props.gitToplevel}
        homeDir={worker.getHomeDir()}
        flavor={workerFlavor()}
      />
      <GitOptionsLoader gitInfo={pathInfo}>
        {() => (
          <>
            <GitOptions
              workerId={worker.workerId()}
              selectedPath={worker.workingDir()}
              homeDir={worker.getHomeDir()}
              gitInfo={pathInfo}
              gitMode={gitMode.gitMode}
              onGitModeChange={gitMode.handleGitModeChange}
              // The constant, not a hand-copied literal: `isChangeBranchMode`
              // above validates the submitted intent against it, so a spelled
              // list here could offer a radio the submit path then refuses.
              modes={CHANGE_BRANCH_MODES}
              preloadedBranches={inspect.branches}
              preloadedBranchesLoading={inspect.branchesLoading}
              onRefreshBranches={inspect.refresh}
            />

            <Show when={gitMode.gitMode() === GitMode.CreateWorktree}>
              <div>
                <div class={labelRow}>Open as</div>
                {/* Two options, so pills rather than a menu -- the same
                    threshold EnumControl uses. */}
                <PillGroup
                  label="Open as"
                  options={[
                    { value: TabType.AGENT, label: 'Agent' },
                    { value: TabType.TERMINAL, label: 'Terminal' },
                  ]}
                  selected={v => v === worktreeTabType()}
                  onSelect={v => setWorktreeTabType(v as WorktreeTabType)}
                />
              </div>
              <Switch>
                <Match when={worktreeTabType() === TabType.AGENT}>
                  <Show when={noProviders()}>
                    <div class={errorText}>
                      No agent providers configured for this worker.
                    </div>
                  </Show>
                  <AgentProviderSelector
                    value={agentProvider}
                    onChange={setAgentProvider}
                    availableProviders={props.availableProviders}
                    onRefresh={props.onRefreshProviders}
                  />
                </Match>
                <Match when={worktreeTabType() === TabType.TERMINAL}>
                  <ShellSelector state={shellState} />
                </Match>
              </Switch>
              <TitleInput state={title} />
            </Show>

            <Show when={gitMode.gitMode() === GitMode.SwitchBranch || gitMode.gitMode() === GitMode.CreateBranch}>
              <div class={warningText}>
                Running agents and terminals will continue on the new branch.
              </div>
            </Show>
          </>
        )}
      </GitOptionsLoader>
    </WorkerDialogShell>
  )
}
