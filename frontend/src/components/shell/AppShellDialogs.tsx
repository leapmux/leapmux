import type { Component } from 'solid-js'
import type { LastTabConfirmState } from './LastTabCloseDialog'
import type { TabContext } from './tabContext'
import type { useAgentOperations } from './useAgentOperations'
import type { useTabOperations } from './useTabOperations'
import type { useTerminalOperations } from './useTerminalOperations'
import type { AgentInfo, AgentProvider } from '~/generated/leapmux/v1/agent_pb'
import type { DialogState, ToggleDialogState } from '~/hooks/createDialogState'
import type { KeyPinDecision } from '~/lib/channel'
import type { createLayoutStore } from '~/stores/layout.store'
import type { createSectionStore } from '~/stores/section.store'
import type { createTabStore } from '~/stores/tab.store'
import type { Tab } from '~/stores/tab.types'
import type { WorkspaceStoreRegistryType } from '~/stores/workspaceStoreRegistry'
import { Show } from 'solid-js'
import { sectionClient } from '~/api/clients'
import { ConfirmDialog } from '~/components/common/ConfirmDialog'
import { KeyPinMismatchDialog } from '~/components/common/KeyPinMismatchDialog'
import { ChangeBranchDialog } from '~/components/workspace/ChangeBranchDialog'
import { DeleteBranchDialog } from '~/components/workspace/DeleteBranchDialog'
import { NewWorkspaceDialog } from '~/components/workspace/NewWorkspaceDialog'
import { TerminalStatus } from '~/generated/leapmux/v1/terminal_pb'
import { TabType } from '~/generated/leapmux/v1/workspace_pb'
import { mid } from '~/lib/lexorank'
import { protoToAgentTabFields } from '~/stores/tab.helpers'
import { LastTabCloseDialog } from './LastTabCloseDialog'
import { NewAgentDialog } from './NewAgentDialog'
import { NewTerminalDialog } from './NewTerminalDialog'

export interface KeyPinConfirmState {
  workerId: string
  expectedFingerprint: string
  actualFingerprint: string
  resolve: (decision: KeyPinDecision) => void
}

export interface ChangeBranchState {
  workerId: string
  /**
   * `git rev-parse --show-toplevel` of the branch group's working dir.
   * For a main repo this is the repo root; for a linked worktree it's
   * the worktree root. Matches `Tab.gitToplevel`.
   */
  gitToplevel: string
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

export interface DeleteBranchState {
  workerId: string
  /** See ChangeBranchState.gitToplevel. */
  gitToplevel: string
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
  lastTabConfirm: DialogState<LastTabConfirmState>
  keyPinConfirm: DialogState<KeyPinConfirmState>
  changeBranch: DialogState<ChangeBranchState>
  deleteBranch: DialogState<DeleteBranchState>
}

interface AppShellDialogsProps {
  dialogs: AppShellDialogStates
  /**
   * Called after a successful Change branch / non-worktree Delete
   * branch with the branch the working directory is now on. The
   * parent stamps every tab in `(workerId, gitToplevel)` with the new
   * label and, if that repo is the active tab's repo, refreshes the
   * gitFileStatusStore so diff stats track the new HEAD.
   */
  onBranchChanged?: (workerId: string, gitToplevel: string, newBranch: string) => void
  activeWorkspace: () => { id: string } | null
  getCurrentTabContext: () => TabContext
  agentOps: ReturnType<typeof useAgentOperations>
  termOps: ReturnType<typeof useTerminalOperations>
  tabOps: ReturnType<typeof useTabOperations>
  tabStore: ReturnType<typeof createTabStore>
  layoutStore: ReturnType<typeof createLayoutStore>
  sectionStore: ReturnType<typeof createSectionStore>
  registry: WorkspaceStoreRegistryType
  focusEditor: () => void
  orgSlug: string
  loadWorkspaces: () => Promise<void>
  navigate: (path: string) => void
  availableProviders?: AgentProvider[]
  onRefreshProviders?: () => void
}

export const AppShellDialogs: Component<AppShellDialogsProps> = (props) => {
  // Full per-agent metadata lives on the Tab record now;
  // protoToAgentTabFields also primes settingsLabelCache.
  const addAgentTabToFocusedTile = (agent: AgentInfo) => {
    const tileId = props.layoutStore.focusedTileId()
    const afterKey = props.tabStore.getActiveTabKeyForTile(tileId)
    props.tabStore.addTab({
      type: TabType.AGENT,
      id: agent.id,
      tileId,
      ...protoToAgentTabFields(agent.workerId, agent),
    }, { afterKey })
    props.tabStore.setActiveTabForTile(tileId, TabType.AGENT, agent.id)
  }

  const addTerminalTabToFocusedTile = (terminalId: string, workerId: string, workingDir: string, title: string) => {
    const tileId = props.layoutStore.focusedTileId()
    const afterKey = props.tabStore.getActiveTabKeyForTile(tileId)
    props.tabStore.addTab({ type: TabType.TERMINAL, id: terminalId, title, tileId, workerId, workingDir, status: TerminalStatus.READY }, { afterKey })
    props.tabStore.setActiveTabForTile(tileId, TabType.TERMINAL, terminalId)
  }

  return (
    <>
      <Show when={props.dialogs.newAgent.isOpen()}>
        <NewAgentDialog
          workspaceId={props.activeWorkspace()?.id ?? ''}
          defaultWorkerId={props.getCurrentTabContext().workerId}
          defaultWorkingDir={props.getCurrentTabContext().workingDir}
          availableProviders={props.availableProviders}
          onRefreshProviders={props.onRefreshProviders}
          onCreated={(agent) => {
            props.dialogs.newAgent.close()
            addAgentTabToFocusedTile(agent)
            requestAnimationFrame(() => props.focusEditor())
          }}
          onClose={() => props.dialogs.newAgent.close()}
        />
      </Show>

      <Show when={props.dialogs.newTerminal.isOpen()}>
        <NewTerminalDialog
          workspaceId={props.activeWorkspace()?.id ?? ''}
          defaultWorkerId={props.getCurrentTabContext().workerId}
          defaultWorkingDir={props.getCurrentTabContext().workingDir}
          onCreated={(terminalId, workerId, workingDir, title) => {
            props.dialogs.newTerminal.close()
            if (!props.activeWorkspace())
              return
            addTerminalTabToFocusedTile(terminalId, workerId, workingDir, title)
          }}
          onClose={() => props.dialogs.newTerminal.close()}
        />
      </Show>

      <Show when={props.dialogs.newWorkspace.value()}>
        {payload => (
          <NewWorkspaceDialog
            preselectedWorkerId={payload().preselectedWorkerId}
            availableProviders={props.availableProviders}
            onRefreshProviders={props.onRefreshProviders}
            registry={props.registry}
            onCreated={(workspaceId) => {
              const targetSectionId = payload().targetSectionId ?? null
              props.dialogs.newWorkspace.close()
              const refreshWorkspaces = props.loadWorkspaces
              if (targetSectionId) {
                // Append past the section's existing items so the new
                // workspace gets a unique lexorank rather than colliding
                // with whichever item already sits at lexorank.first()
                // ("n"). A hardcoded 'N' position would land every new
                // workspace at the same rank, and the SQL planner would
                // then shuffle the tied rows on every page refresh.
                const lastItem = props.sectionStore.getItemsForSection(targetSectionId).at(-1)
                const position = lastItem ? mid(lastItem.position, '') : mid('', '')
                sectionClient.moveWorkspace({
                  workspaceId,
                  sectionId: targetSectionId,
                  position,
                }).catch(() => {}).finally(() => {
                  refreshWorkspaces()
                })
              }
              else {
                refreshWorkspaces()
              }
              props.navigate(`/o/${props.orgSlug}/workspace/${workspaceId}`)
            }}
            onClose={() => props.dialogs.newWorkspace.close()}
          />
        )}
      </Show>

      <Show when={props.dialogs.confirmDeleteWs.value()}>
        {state => (
          <ConfirmDialog
            title="Delete workspace"
            confirmLabel="Delete"
            danger
            onConfirm={() => {
              state().resolve(true)
              props.dialogs.confirmDeleteWs.close()
            }}
            onCancel={() => {
              state().resolve(false)
              props.dialogs.confirmDeleteWs.close()
            }}
          >
            <p>Are you sure you want to delete this workspace? This cannot be undone.</p>
          </ConfirmDialog>
        )}
      </Show>

      <Show when={props.dialogs.confirmArchiveWs.value()}>
        {state => (
          <ConfirmDialog
            title="Archive workspace"
            confirmLabel="Archive"
            onConfirm={() => {
              state().resolve(true)
              props.dialogs.confirmArchiveWs.close()
            }}
            onCancel={() => {
              state().resolve(false)
              props.dialogs.confirmArchiveWs.close()
            }}
          >
            <p>Are you sure you want to archive this workspace? All active agents and terminals will be stopped.</p>
          </ConfirmDialog>
        )}
      </Show>

      <Show when={props.dialogs.lastTabConfirm.value()}>
        {confirm => (
          <LastTabCloseDialog
            state={confirm()}
            onDismiss={() => props.dialogs.lastTabConfirm.close()}
            onStatusRefreshed={refreshed => props.dialogs.lastTabConfirm.update(refreshed)}
          />
        )}
      </Show>

      <Show when={props.dialogs.keyPinConfirm.value()}>
        {state => (
          <KeyPinMismatchDialog
            workerId={state().workerId}
            expectedFingerprint={state().expectedFingerprint}
            actualFingerprint={state().actualFingerprint}
            resolve={(decision) => {
              state().resolve(decision)
              props.dialogs.keyPinConfirm.close()
            }}
          />
        )}
      </Show>

      <Show when={props.dialogs.changeBranch.value()}>
        {state => (
          <ChangeBranchDialog
            workerId={state().workerId}
            gitToplevel={state().gitToplevel}
            workspaceId={state().workspaceId}
            branchName={state().branchName}
            isWorktree={state().isWorktree}
            availableProviders={props.availableProviders}
            onRefreshProviders={props.onRefreshProviders}
            onBranchChanged={newBranch => props.onBranchChanged?.(state().workerId, state().gitToplevel, newBranch)}
            // Local-UI tab insertion only applies when the dialog's
            // target workspace IS the active one — addAgentTabToFocusedTile
            // and addTerminalTabToFocusedTile write into the ACTIVE
            // workspace's tabStore + layoutStore, so calling them on a
            // dialog opened against a non-active workspace's branch row
            // would land the new tab in the wrong workspace's tree. For
            // non-active dialogs the new tab still arrives in the target
            // workspace via its CRDT projection on the next refresh; no
            // immediate local UI write is needed (and the user isn't
            // looking at that workspace's tile to feel the latency).
            onAgentCreated={(agent) => {
              if (state().workspaceId === props.activeWorkspace()?.id)
                addAgentTabToFocusedTile(agent)
            }}
            onTerminalCreated={(terminalId, workerId, workingDir, title) => {
              if (state().workspaceId === props.activeWorkspace()?.id)
                addTerminalTabToFocusedTile(terminalId, workerId, workingDir, title)
            }}
            onClose={() => props.dialogs.changeBranch.close()}
          />
        )}
      </Show>

      <Show when={props.dialogs.deleteBranch.value()}>
        {state => (
          <DeleteBranchDialog
            workerId={state().workerId}
            gitToplevel={state().gitToplevel}
            branchName={state().branchName}
            tabs={state().tabs}
            closeWorktreeTabs={props.tabOps.closeWorktreeTabs}
            onBranchChanged={newBranch => props.onBranchChanged?.(state().workerId, state().gitToplevel, newBranch)}
            onClose={() => props.dialogs.deleteBranch.close()}
          />
        )}
      </Show>
    </>
  )
}
