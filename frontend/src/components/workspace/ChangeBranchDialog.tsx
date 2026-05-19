import type { Component } from 'solid-js'
import type { AgentInfo, AgentProvider } from '~/generated/leapmux/v1/agent_pb'
import { createSignal, Match, Show, Switch } from 'solid-js'
import * as workerRpc from '~/api/workerRpc'
import { labelRow } from '~/components/common/Dialog.css'
import { AgentProviderSelector } from '~/components/shell/AgentProviderSelector'
import { isChangeBranchSubmitDisabled } from '~/components/shell/dialogValidation'
import { GitOptions } from '~/components/shell/GitOptions'
import { GitOptionsLoader } from '~/components/shell/GitOptionsLoader'
import { ShellSelect } from '~/components/shell/ShellSelect'
import { DialogFormFooter, WorkerDialogShell } from '~/components/shell/WorkerDialogShell'
import { resolveStampedBranch } from '~/components/workspace/branchStamp'
import { useOrg } from '~/context/OrgContext'
import { TabType } from '~/generated/leapmux/v1/workspace_pb'
import { createWorkerDialogContext } from '~/hooks/createWorkerDialogContext'
import { useAgentProviderSelection } from '~/hooks/useAgentProviderSelection'
import { useAvailableShells } from '~/hooks/useAvailableShells'
import { useChangeBranchInspect } from '~/hooks/useChangeBranchInspect'
import { useDialogSubmit } from '~/hooks/useDialogSubmit'
import { fieldsForCreateWorktree, GitMode, isChangeBranchMode, useGitModeState } from '~/hooks/useGitModeState'
import { formatErrorMessage } from '~/lib/errors'
import { createLogger } from '~/lib/logger'
import { DEFAULT_TERMINAL_COLS, DEFAULT_TERMINAL_ROWS } from '~/lib/terminal'
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
  /** Workspace to associate any new agent tab with (worktree mode). */
  workspaceId: string
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
  availableProviders?: AgentProvider[]
  onRefreshProviders?: () => void
  /**
   * Notified after a successful in-place checkout or branch creation
   * with the local branch the working directory is now on. Parents
   * route this into `tabStore.stampBranchOnTabs` (which carries the
   * rationale for why direct stamping is needed).
   */
  onBranchChanged?: (newBranch: string) => void
  onAgentCreated?: (agent: AgentInfo) => void
  onTerminalCreated?: (terminalId: string, workerId: string, workingDir: string, title: string) => void
  onClose: () => void
}

export const ChangeBranchDialog: Component<ChangeBranchDialogProps> = (props) => {
  const org = useOrg()
  /* eslint-disable solid/reactivity -- initial-mount snapshot; the dialog is opened against a fixed (workerId, gitToplevel) and stays on them */
  const { submitting, error, setError, formHandler } = useDialogSubmit()
  const worker = createWorkerDialogContext({
    singleWorkerId: props.workerId,
    defaultWorkingDir: props.gitToplevel,
    onError: setError,
  })
  // The dialog renders SwitchBranch / CreateBranch / CreateWorktree
  // (Current is intentionally excluded). Seed the parent intent so
  // GitOptions paints SwitchBranch selected on first render.
  const gitMode = useGitModeState({ mode: GitMode.SwitchBranch, checkoutBranch: '', checkoutBranchError: null })
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

  const { shells, defaultShell, shell, setShell, loading: shellsLoading } = useAvailableShells(
    () => {
      if (gitMode.gitMode() === GitMode.CreateWorktree && worktreeTabType() === TabType.TERMINAL) {
        return {
          orgId: org.orgId(),
          workspaceId: props.workspaceId,
          workerId: props.workerId,
        }
      }
      return null
    },
    err => log.warn('Failed to list shells', err),
  )

  const submitDisabled = () => isChangeBranchSubmitDisabled({
    submitting: submitting.loading(),
    // SwitchBranch intent now carries `checkoutBranchError` set by
    // GitOptions when the destination resolves to the current branch
    // (the path-info probe's currentBranch is the source of truth, and
    // it lives in GitOptions where the branches-list lookup also lives).
    // No extra plumbing needed here.
    git: gitMode.currentIntent(),
    worktreeTabType: worktreeTabType(),
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
      orgId: org.orgId(),
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
        const worktreeArgs = {
          workspaceId: props.workspaceId,
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
            model: '',
            systemPrompt: '',
          })
          if (resp.agent) {
            recordProviderUse(provider)
            safeCallback('onAgentCreated', props.onAgentCreated, resp.agent)
          }
          return
        }
        const resp = await workerRpc.openTerminal(props.workerId, {
          ...worktreeArgs,
          orgId: org.orgId(),
          cols: DEFAULT_TERMINAL_COLS,
          rows: DEFAULT_TERMINAL_ROWS,
          shell: shell(),
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
              modes={[GitMode.SwitchBranch, GitMode.CreateBranch, GitMode.CreateWorktree]}
              preloadedBranches={inspect.branches}
              preloadedBranchesLoading={inspect.branchesLoading}
              onRefreshBranches={inspect.refresh}
            />

            <Show when={gitMode.gitMode() === GitMode.CreateWorktree}>
              <div>
                <div class={labelRow}>Open as</div>
                <select
                  value={worktreeTabType()}
                  onChange={e => setWorktreeTabType(Number(e.currentTarget.value) as WorktreeTabType)}
                >
                  <option value={TabType.AGENT}>Agent</option>
                  <option value={TabType.TERMINAL}>Terminal</option>
                </select>
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
                  <div>
                    <div class={labelRow}>Shell</div>
                    <ShellSelect
                      value={shell()}
                      onChange={setShell}
                      shells={shells()}
                      defaultShell={defaultShell()}
                      loading={shellsLoading()}
                    />
                  </div>
                </Match>
              </Switch>
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
