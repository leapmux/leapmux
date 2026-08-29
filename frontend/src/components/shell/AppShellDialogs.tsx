import type { Component } from 'solid-js'
import type { LastTabConfirmState } from './LastTabCloseDialog'
import type { TabContext } from './tabContext'
import type { useAgentOperations } from './useAgentOperations'
import type { useTabOperations } from './useTabOperations'
import type { useTerminalOperations } from './useTerminalOperations'
import type { AgentInfo, AgentProvider } from '~/generated/leapmux/v1/agent_pb'
import type { DialogState, ToggleDialogState, UpdatableDialogState } from '~/hooks/createDialogState'
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
import { ChangeBranchDialog } from '~/components/workspace/ChangeBranchDialog'
import { DeleteBranchDialog } from '~/components/workspace/DeleteBranchDialog'
import { NewWorkspaceDialog } from '~/components/workspace/NewWorkspaceDialog'
import { TabType } from '~/generated/leapmux/v1/workspace_pb'
import { openedAgentTabFields, openedTerminalMetadata, planOptimisticRepoGit } from '~/stores/tab.helpers'
import { LastTabCloseDialog } from './LastTabCloseDialog'
import { NewAgentDialog } from './NewAgentDialog'
import { NewTerminalDialog } from './NewTerminalDialog'
import { hasPlaceableTab, openTabInFocusedTile } from './openTabInFocusedTile'
import { placeWorkspaceInSection } from './placeWorkspaceInSection'

export interface KeyPinConfirmState {
  workerId: string
  expectedFingerprint: string
  actualFingerprint: string
  resolve: (decision: KeyPinDecision) => void
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
}

export interface DeleteBranchState extends RepoRef {
  /**
   * Current branch label, threaded so the dialog can seed its path-info
   * snapshot and skip the mount-time getGitInfo probe. `null` when the
   * tab group has no current branch (sidebar's "(no branch)" bucket).
   */
  branchName: string | null
  tabs: Tab[]
}

export interface WorkspaceConfirmPayload {
  workspaceId: string
  resolve: (confirmed: boolean) => void
}

/**
 * Open-time payload for the NewWorkspaceDialog. Both fields are optional:
 *   - `preselectedWorkerId` seeds the worker dropdown (`?newWorkspace=true&workerId=`
 *     from the URL, or the workspace-sidebar "+ workspace" button on a specific worker).
 *   - `targetSectionId` is the section the freshly-created workspace will be moved into
 *     post-CreateWorkspace (a left-sidebar "+" inside a section header).
 * The shortcut path opens with `{}` (no preselection, default section).
 */
export interface NewWorkspacePayload {
  preselectedWorkerId?: string
  targetSectionId?: string | null
}

/**
 * All app-shell dialog handles, bundled at the AppShell boundary so adding
 * a new dialog touches three places (AppShell creation, this prop, the
 * dialog component) instead of threading a fresh show/set pair through
 * every layer.
 */
export interface AppShellDialogStates {
  newAgent: ToggleDialogState
  newTerminal: ToggleDialogState
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
  isActiveWorkspaceMutatable: () => boolean
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
    // an orphan that no tab reads and nothing reclaims.
    if (placedTileId)
      seed.commit()
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
    if (placedTileId)
      seed.commit()
  }

  /**
   * Why a new tab cannot be created right now, or undefined when it can.
   * A tab is placed onto the active workspace's projected tree, so the
   * workspace, its mutatability, and its tree are preconditions — the
   * dialogs disable submit on this reason instead of creating the
   * worker-side agent/pty first and orphaning it when placement refuses.
   */
  const newTabBlockedReason = (): string | undefined => {
    if (!props.activeWorkspace())
      return 'Create a workspace first — a tab lives inside a workspace.'
    if (!props.isActiveWorkspaceMutatable())
      return 'This workspace is archived. Unarchive it to create tabs.'
    if (!hasPlaceableTab(props.layoutStore))
      return 'The workspace view is not ready yet. Try again in a moment.'
    return undefined
  }

  return (
    <>
      <Show when={props.dialogs.newAgent.isOpen()}>
        <NewAgentDialog
          defaultWorkerId={props.getCurrentTabContext().workerId}
          defaultWorkingDir={props.getCurrentTabContext().workingDir}
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
      </Show>

      <Show when={props.dialogs.newTerminal.isOpen()}>
        <NewTerminalDialog
          defaultWorkerId={props.getCurrentTabContext().workerId}
          defaultWorkingDir={props.getCurrentTabContext().workingDir}
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
            preselectedWorkerId={payload.preselectedWorkerId}
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
            workspaceId={state.workspaceId}
            branchName={state.branchName}
            isWorktree={state.isWorktree}
            availableProviders={props.availableProviders}
            onRefreshProviders={props.onRefreshProviders}
            onBranchChanged={newBranch => props.onBranchChanged?.(state, newBranch)}
            // The guard reason describes the ACTIVE workspace's placement
            // (the same condition the onAgentCreated/onTerminalCreated
            // callbacks below gate on) — a dialog opened against another
            // workspace's branch row places no local tab, so no reason
            // applies to it.
            blockedReason={() => state.workspaceId === props.activeWorkspace()?.id
              ? newTabBlockedReason()
              : undefined}
            // Local-UI tab insertion only applies when the dialog's
            // target workspace IS the active one — addAgentTabToFocusedTile
            // and addTerminalTabToFocusedTile place the tab on the ACTIVE
            // workspace's focused tile, so calling them on a
            // dialog opened against a non-active workspace's branch row
            // would land the new tab in the wrong workspace's tree. For
            // non-active dialogs the new tab still arrives in the target
            // workspace via its CRDT projection on the next refresh; no
            // immediate local UI write is needed (and the user isn't
            // looking at that workspace's tile to feel the latency).
            onAgentCreated={(agent) => {
              // No git seed. This dialog reaches the agent branch only in
              // CreateWorktree mode, so the agent lands in a NEW worktree on a
              // different branch than the active tab shows.
              if (state.workspaceId === props.activeWorkspace()?.id)
                addAgentTabToFocusedTile(agent, { seedGitFromActiveTab: false })
            }}
            onTerminalCreated={(terminalId, workerId, workingDir, title) => {
              if (state.workspaceId === props.activeWorkspace()?.id)
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
