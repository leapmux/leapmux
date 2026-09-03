import type { Component } from 'solid-js'
import type { LastTabConfirmState } from './LastTabCloseDialog'
import type { TabContext } from './tabContext'
import type { useAgentOperations } from './useAgentOperations'
import type { useTabOperations } from './useTabOperations'
import type { useTerminalOperations } from './useTerminalOperations'
import type { WorkspaceStartPoint } from '~/components/workspace/workspaceStartPoint'
import type { AgentInfo, AgentProvider } from '~/generated/proto/leapmux/v1/agent_pb'
import type { DialogState, UpdatableDialogState } from '~/hooks/createDialogState'
import type { ChangeBranchMode } from '~/hooks/useGitModeState'
import type { KeyPinDecision } from '~/lib/keyPinStore'
import type { createLayoutStore } from '~/stores/layout.store'
import type { createRepoGitStore } from '~/stores/repoGit.store'
import type { createSectionStore } from '~/stores/section.store'
import type { RepoRef } from '~/stores/tab.helpers'
import type { Tab } from '~/stores/tab.types'
import type { TabMetadataStore } from '~/stores/tabMetadata.store'
import type { TabSelectionStore } from '~/stores/tabSelection.store'
import type { TabView } from '~/stores/tabView'
import { Show } from 'solid-js'
import { ConfirmDialog } from '~/components/common/ConfirmDialog'
import { KeyPinMismatchDialog } from '~/components/common/KeyPinMismatchDialog'
import { showWarnToast } from '~/components/common/Toast'
import { ChangeBranchDialog } from '~/components/workspace/ChangeBranchDialog'
import { DeleteBranchDialog } from '~/components/workspace/DeleteBranchDialog'
import { NewWorkspaceDialog } from '~/components/workspace/NewWorkspaceDialog'
import { TabType } from '~/generated/proto/leapmux/v1/workspace_pb'
import { openedAgentTabFields, openedTerminalMetadata, planOptimisticRepoGit } from '~/stores/tab.helpers'
import { LastTabCloseDialog } from './LastTabCloseDialog'
import { NewAgentDialog } from './NewAgentDialog'
import { NewTerminalDialog } from './NewTerminalDialog'
import { openTabInFocusedTile } from './openTabInFocusedTile'
import { placeWorkspaceInSection } from './placeWorkspaceInSection'

export interface KeyPinConfirmState {
  workerId: string
  expectedFingerprint: string
  actualFingerprint: string
  resolve: (decision: KeyPinDecision) => void
}

/**
 * Where a new agent or terminal starts.
 *
 * Both fields are optional and travel together in practice: an EMPTY target
 * means "follow the current tab context", which is what the tab bar's own
 * new-tab actions and the keyboard shortcuts want. The branch context menu
 * fills both in from the branch it acts on, so the dialog opens on that
 * checkout rather than on whatever tab happens to be focused.
 */
export interface NewTabTarget {
  workerId?: string
  workingDir?: string
}

export interface ChangeBranchState extends RepoRef {
  workspaceId: string
  /**
   * Current branch label on the row that opened the dialog. Threaded so
   * the dialog can seed its path-info snapshot synchronously instead of
   * flashing an empty `currentBranch` until the probe lands. `null`
   * when the row groups tabs that have no current branch.
   */
  branchName: string | null
  /**
   * True iff `gitToplevel` resolves to a linked worktree (mirrors the
   * sidebar's BranchGroup.isWorktree). Threaded so the dialog can seed
   * `isWorktreeRoot` / `isRepoRoot` correctly before the inspect RPC
   * lands — without this, a worktree-opened dialog would briefly paint
   * a main-repo shape and any GitOptions memo branching on the seeded
   * fields (e.g. the suggested worktree-path computation) would compute
   * against the wrong values until the RPC corrects them.
   */
  isWorktree: boolean
  /**
   * Which of the three git modes the dialog opens on. The branch context menu
   * offers one item per mode, so the item the user picked decides the radio.
   */
  initialMode: ChangeBranchMode
}

export interface DeleteBranchState extends RepoRef {
  /**
   * Current branch label, threaded so the dialog can seed its path-info
   * snapshot and skip the mount-time getGitInfo probe. `null` when the
   * tab group has no current branch (sidebar's "(no branch)" bucket).
   */
  branchName: string | null
  /**
   * True iff `gitToplevel` resolves to a linked worktree (mirrors the sidebar's
   * BranchGroup.isWorktree, exactly as `ChangeBranchState.isWorktree` does).
   * The dialog seeds its title, its submit label and its status rows from it,
   * so the first paint never calls a worktree a branch — a delete that removes
   * a whole directory must not announce itself as a branch delete.
   */
  isWorktree: boolean
  tabs: Tab[]
}

export interface WorkspaceConfirmPayload {
  workspaceId: string
  resolve: (confirmed: boolean) => void
}

/**
 * Open-time payload for the NewWorkspaceDialog. Both fields are optional:
 *   - `startPoint` says what the dialog already knows: a repository the section
 *     works on (from a section header menu row), or a worker alone
 *     (`?newWorkspace=true&workerId=` from the URL). Omit it for "no target",
 *     which is the same thing as `{ kind: 'directory' }` -- the default is
 *     applied at ONE place, where this payload reaches the dialog.
 *   - `targetSectionId` is the section the freshly-created workspace will be
 *     moved into post-CreateWorkspace (a section header menu's own section).
 * The shortcut path opens with `{}` (no target, default section).
 */
export interface NewWorkspacePayload {
  startPoint?: WorkspaceStartPoint
  targetSectionId?: string | null
}

/**
 * All app-shell dialog handles, bundled at the AppShell boundary so adding
 * a new dialog touches three places (AppShell creation, this prop, the
 * dialog component) instead of threading a fresh show/set pair through
 * every layer.
 */
export interface AppShellDialogStates {
  // Payload dialogs, not toggles: a caller says WHERE the new tab starts. An
  // empty payload keeps the old behavior (follow the current tab context).
  newAgent: DialogState<NewTabTarget>
  newTerminal: DialogState<NewTabTarget>
  newWorkspace: DialogState<NewWorkspacePayload>
  confirmDeleteWs: DialogState<WorkspaceConfirmPayload>
  confirmArchiveWs: DialogState<WorkspaceConfirmPayload>
  // The only updatable one: LastTabCloseDialog patches its own payload after
  // a status refresh. That capability is what keeps its <Show> non-keyed.
  lastTabConfirm: UpdatableDialogState<LastTabConfirmState>
  keyPinConfirm: DialogState<KeyPinConfirmState>
  changeBranch: DialogState<ChangeBranchState>
  deleteBranch: DialogState<DeleteBranchState>
}

interface AppShellDialogsProps {
  dialogs: AppShellDialogStates
  /**
   * Called after a successful Change branch / non-worktree Delete
   * branch with the branch the working directory is now on. The
   * parent updates the repo-keyed git store with the new branch label and
   * refreshes diff stats for the affected repo.
   */
  onBranchChanged?: (repo: RepoRef, newBranch: string) => void
  activeWorkspace: () => { id: string } | null
  /** False while the active workspace is archived — read-only, so no new tabs. */
  /**
   * Whether a NAMED workspace takes mutation. Per id, not per active workspace:
   * a dialog can act on a workspace the user is not looking at, and the guard
   * has to answer for the one the RPC is for.
   */
  isWorkspaceMutatable: (workspaceId: string) => boolean
  getCurrentTabContext: () => TabContext
  agentOps: ReturnType<typeof useAgentOperations>
  termOps: ReturnType<typeof useTerminalOperations>
  tabOps: ReturnType<typeof useTabOperations>
  view: TabView
  metadata: TabMetadataStore
  selection: TabSelectionStore
  layoutStore: ReturnType<typeof createLayoutStore>
  sectionStore: ReturnType<typeof createSectionStore>
  focusEditor: () => void
  loadWorkspaces: () => Promise<void>
  /** Makes a workspace the active one. There is no per-workspace URL to go to. */
  onSelectWorkspace: (id: string) => void
  availableProviders?: AgentProvider[]
  onRefreshProviders?: () => void
  repoGitStore: ReturnType<typeof createRepoGitStore>
}

export const AppShellDialogs: Component<AppShellDialogsProps> = (props) => {
  // Full per-agent metadata lives on the Tab record now. `openedAgentTabFields`
  // also primes settingsLabelCache, and it carries `hydrated: true`: this
  // `AgentInfo` came from the worker, so the hydrator has nothing to add and
  // must not re-ask (see `TabMetadata.hydrated`).
  //
  // `seedGitFromActiveTab` copies the active tab's branch onto the new tab
  // while the worker's own status is on its way. The OpenAgent response carries
  // no git status -- the worker computes it in startup phase 1 and sends it on
  // the STARTING broadcast -- so without the seed the tab renders outside its
  // repository group for the whole startup. `useAgentOperations` already seeds
  // on the tab-bar path.
  //
  // Only the caller knows whether the seed is safe. It is safe only when the
  // agent lands in the directory the active tab is showing, which the plain
  // git mode is the only one to guarantee.
  /** The active tab to seed git from, or undefined when seeding is unsafe. */
  const gitSeedSource = (seedGitFromActiveTab: boolean) =>
    seedGitFromActiveTab
      ? props.selection.activeTabForWorkspace(props.activeWorkspace()?.id ?? '')
      : undefined

  const addAgentTabToFocusedTile = (agent: AgentInfo, opts: { seedGitFromActiveTab: boolean }) => {
    // `resolveOptimisticGitInfo`'s own guard still applies: it copies nothing
    // unless the two tabs resolve to the same git directory.
    const seed = planOptimisticRepoGit(
      props.repoGitStore,
      gitSeedSource(opts.seedGitFromActiveTab),
      { workerId: agent.workerId, workingDir: agent.workingDir },
    )
    const placedTileId = openTabInFocusedTile(
      props,
      { type: TabType.AGENT, id: agent.id, workerId: agent.workerId },
      // The seed first, so the worker's own answer wins wherever it has one.
      { ...seed.fields, ...openedAgentTabFields(props.repoGitStore, agent) },
    )
    // Placement can be refused, and a store entry written before it would be
    // an orphan that no tab reads and nothing reclaims. Say so: the agent
    // already exists on the Worker by the time this runs, so a silent return
    // leaves a running process the user was never told about. The pre-check in
    // `newTabBlockedReason` is what usually prevents this; the toast covers the
    // window it cannot.
    if (placedTileId)
      seed.commit()
    else
      showWarnToast('Cannot open the agent', new Error('The workspace is not ready for a new tab yet.'))
  }

  // This path seeds from `workingDir` alone. It differs from
  // `useTerminalOperations`, whose open RPC reports a `shellStartDir` the
  // seed receives: the dialog's openTerminal RPC sends no start directory, so
  // there is none to thread, and the seed's directory guard compares against
  // the working directory on both sides. `seedGitFromActiveTab` is true only
  // in the plain use-this-directory mode, whose seed fields are empty, so
  // nothing is seeded from a mismatched directory today.
  const addTerminalTabToFocusedTile = (
    terminalId: string,
    workerId: string,
    workingDir: string,
    title: string,
    opts: { seedGitFromActiveTab: boolean } = { seedGitFromActiveTab: false },
  ) => {
    const seed = planOptimisticRepoGit(
      props.repoGitStore,
      gitSeedSource(opts.seedGitFromActiveTab),
      { workerId, workingDir },
    )
    const placedTileId = openTabInFocusedTile(
      props,
      { type: TabType.TERMINAL, id: terminalId, workerId },
      { ...seed.fields, ...openedTerminalMetadata({ title, workingDir }) },
    )
    // See `addAgentTabToFocusedTile`: the pty is already running by now, so a
    // refused placement has to be reported rather than swallowed.
    if (placedTileId)
      seed.commit()
    else
      showWarnToast('Cannot open the terminal', new Error('The workspace is not ready for a new tab yet.'))
  }

  /**
   * Why a new tab cannot be created right now, or undefined when it can.
   * A tab is placed onto the active workspace's projected tree, so the
   * workspace, its mutatability, and its tree are preconditions — the
   * dialogs disable submit on this reason instead of creating the
   * worker-side agent/pty first and orphaning it when placement refuses.
   */
  const newTabBlockedReason = (workspaceId?: string): string | undefined => {
    const active = props.activeWorkspace()
    if (!active)
      return 'Create a workspace first — a tab lives inside a workspace.'
    // A dialog opened from another workspace's branch row places into THAT
    // workspace, so the readiness question has to be asked about it. Answering
    // `undefined` for it -- which is what "no reason applies to a non-active
    // workspace" amounted to -- left Create enabled over a tree the projection
    // had not delivered, and the refusal that follows is silent: the RPC has
    // already made the agent and the worktree, and nothing places a tab.
    const target = workspaceId ?? active.id
    // Asked about the TARGET, not only when the target IS the active workspace.
    // An archived workspace takes no tab either way, and a branch row of one is
    // reachable while another workspace is active -- so scoping this to
    // `target === active.id` left Create enabled over exactly the workspace
    // that refuses the tab, and the refusal arrives only after the RPC has made
    // the agent.
    if (!props.isWorkspaceMutatable(target))
      return 'This workspace is archived. Unarchive it to create tabs.'
    // `firstLeafIdFor` answers for ANY workspace and is null exactly when
    // `placementTileId()` would be empty, which is the state this reason names.
    if (!props.layoutStore.firstLeafIdFor(target))
      return 'The workspace view is not ready yet. Try again in a moment.'
    return undefined
  }

  return (
    <>
      {/* Both new-tab dialogs are KEYED on their target, like every other
          payload dialog below: a second `open` with a different branch has to
          re-point the dialog rather than leave it on the first one. */}
      <Show when={props.dialogs.newAgent.value()} keyed>
        {target => (
          <NewAgentDialog
            // `||`, not `??`: there is no valid empty worker id or path, so an
            // empty string is the same "unknown" that `undefined` is, and both
            // have to fall through to the tab context.
            defaultWorkerId={target.workerId || props.getCurrentTabContext().workerId}
            defaultWorkingDir={target.workingDir || props.getCurrentTabContext().workingDir}
            availableProviders={props.availableProviders}
            onRefreshProviders={props.onRefreshProviders}
            blockedReason={newTabBlockedReason}
            repoGitStore={props.repoGitStore}
            onCreated={(agent, opts) => {
              props.dialogs.newAgent.close()
              addAgentTabToFocusedTile(agent, opts)
              requestAnimationFrame(() => props.focusEditor())
            }}
            onClose={() => props.dialogs.newAgent.close()}
          />
        )}
      </Show>

      <Show when={props.dialogs.newTerminal.value()} keyed>
        {target => (
          <NewTerminalDialog
            defaultWorkerId={target.workerId || props.getCurrentTabContext().workerId}
            defaultWorkingDir={target.workingDir || props.getCurrentTabContext().workingDir}
            blockedReason={newTabBlockedReason}
            repoGitStore={props.repoGitStore}
            onCreated={(terminalId, workerId, workingDir, title, opts) => {
              props.dialogs.newTerminal.close()
              if (!props.activeWorkspace())
                return
              addTerminalTabToFocusedTile(terminalId, workerId, workingDir, title, opts)
            }}
            onClose={() => props.dialogs.newTerminal.close()}
          />
        )}
      </Show>

      {/* Every payload dialog below renders under a KEYED <Show>, and that is
          load-bearing rather than stylistic. A non-keyed <Show> hands the
          children function an accessor that throws "Stale read from <Show>"
          on any read after the condition goes falsy — which every `close`
          below does. Each of these dialogs reads its payload from inside a
          callback that fires next to that close, so the accessor form leaves
          them correct only by statement order. `keyed` hands over the payload
          itself, so there is nothing left to go stale. It also re-runs the
          children on a payload identity change, so a replacing `open()`
          re-points the dialog instead of freezing on the first payload.

          `lastTabConfirm` is the ONE exception and stays non-keyed: it is the
          only dialog whose payload is patched in place, and `keyed` would
          remount its native <dialog> on every refresh. The type system
          enforces that split — `update` lives on UpdatableDialogState, which
          only that dialog is declared with. */}
      <Show when={props.dialogs.newWorkspace.value()} keyed>
        {payload => (
          <NewWorkspaceDialog
            metadata={props.metadata}
            repoGitStore={props.repoGitStore}
            startPoint={payload.startPoint ?? { kind: 'directory' }}
            availableProviders={props.availableProviders}
            onRefreshProviders={props.onRefreshProviders}
            onCreated={(workspaceId) => {
              props.dialogs.newWorkspace.close()
              placeWorkspaceInSection(
                { sectionStore: props.sectionStore, loadWorkspaces: props.loadWorkspaces },
                workspaceId,
                payload.targetSectionId ?? null,
              )
              props.onSelectWorkspace(workspaceId)
            }}
            onClose={() => props.dialogs.newWorkspace.close()}
          />
        )}
      </Show>

      <Show when={props.dialogs.confirmDeleteWs.value()} keyed>
        {state => (
          <ConfirmDialog
            title="Delete workspace"
            confirmLabel="Delete"
            danger
            onConfirm={() => {
              state.resolve(true)
              props.dialogs.confirmDeleteWs.close()
            }}
            onCancel={() => {
              state.resolve(false)
              props.dialogs.confirmDeleteWs.close()
            }}
          >
            <p>Are you sure you want to delete this workspace? This cannot be undone.</p>
          </ConfirmDialog>
        )}
      </Show>

      <Show when={props.dialogs.confirmArchiveWs.value()} keyed>
        {state => (
          <ConfirmDialog
            title="Archive workspace"
            confirmLabel="Archive"
            onConfirm={() => {
              state.resolve(true)
              props.dialogs.confirmArchiveWs.close()
            }}
            onCancel={() => {
              state.resolve(false)
              props.dialogs.confirmArchiveWs.close()
            }}
          >
            {/* This sentence is NOT true today, and it stays until the behavior
                matches it: archiving only moves the workspace into the archived
                section, so every agent process and every PTY keeps running, and
                the worker's resume sweep starts them again after a restart.
                Correcting the copy alone would hide the defect.
                See https://github.com/leapmux/leapmux/issues/446. */}
            <p>Are you sure you want to archive this workspace? All active agents and terminals will be stopped.</p>
          </ConfirmDialog>
        )}
      </Show>

      {/* NOT keyed — see the block comment above. This is the only dialog
          that patches its payload in place, and keyed would remount it on
          every in-place refresh. */}
      <Show when={props.dialogs.lastTabConfirm.value()}>
        {confirm => (
          <LastTabCloseDialog
            state={confirm()}
            onDismiss={() => props.dialogs.lastTabConfirm.close()}
            onStatusRefreshed={refreshed => props.dialogs.lastTabConfirm.update(refreshed)}
          />
        )}
      </Show>

      <Show when={props.dialogs.keyPinConfirm.value()} keyed>
        {state => (
          <KeyPinMismatchDialog
            workerId={state.workerId}
            expectedFingerprint={state.expectedFingerprint}
            actualFingerprint={state.actualFingerprint}
            resolve={(decision) => {
              state.resolve(decision)
              props.dialogs.keyPinConfirm.close()
            }}
          />
        )}
      </Show>

      <Show when={props.dialogs.changeBranch.value()} keyed>
        {state => (
          <ChangeBranchDialog
            workerId={state.workerId}
            gitToplevel={state.gitToplevel}
            branchName={state.branchName}
            isWorktree={state.isWorktree}
            initialMode={state.initialMode}
            availableProviders={props.availableProviders}
            onRefreshProviders={props.onRefreshProviders}
            onBranchChanged={newBranch => props.onBranchChanged?.(state, newBranch)}
            // The reason answers for the workspace this dialog PLACES into, which
            // is the branch row's own and not necessarily the active one.
            // `openTabInFocusedTile` still refuses a workspace whose tree has not
            // arrived, but that refusal comes AFTER the open RPC has made the
            // agent or the pty -- so the pre-check is the only thing standing
            // between a refused placement and a process with no tab to reach it.
            blockedReason={() => newTabBlockedReason(state.workspaceId)}
            // Select the branch's workspace, THEN place. `addAgentTabToFocusedTile`
            // and `addTerminalTabToFocusedTile` place on the ACTIVE workspace's
            // focused tile, and the switch is one synchronous signal write, so
            // the placement that follows resolves against the right tree.
            //
            // Without the switch the tab was simply never created: the open RPC
            // carries no workspace id (neither request message has a field for
            // one), so nothing server-side files the tab, and the agent or pty
            // stayed alive on the Worker with no tab to reach it by.
            onAgentCreated={(agent) => {
              props.onSelectWorkspace(state.workspaceId)
              // No git seed. This dialog reaches the agent branch only in
              // CreateWorktree mode, so the agent lands in a NEW worktree on a
              // different branch than the active tab shows.
              addAgentTabToFocusedTile(agent, { seedGitFromActiveTab: false })
            }}
            onTerminalCreated={(terminalId, workerId, workingDir, title) => {
              props.onSelectWorkspace(state.workspaceId)
              addTerminalTabToFocusedTile(terminalId, workerId, workingDir, title)
            }}
            onClose={() => props.dialogs.changeBranch.close()}
          />
        )}
      </Show>

      <Show when={props.dialogs.deleteBranch.value()} keyed>
        {state => (
          <DeleteBranchDialog
            workerId={state.workerId}
            gitToplevel={state.gitToplevel}
            branchName={state.branchName}
            isWorktree={state.isWorktree}
            tabs={state.tabs}
            closeWorktreeTabs={props.tabOps.closeWorktreeTabsAndReport}
            onBranchChanged={newBranch => props.onBranchChanged?.(state, newBranch)}
            onClose={() => props.dialogs.deleteBranch.close()}
          />
        )}
      </Show>
    </>
  )
}
