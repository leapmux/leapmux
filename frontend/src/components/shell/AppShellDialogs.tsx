import type { Component } from 'solid-js'
import type { BusyTabConfirmState } from './BusyTabCloseDialog'
import type { LastTabConfirmState } from './LastTabCloseDialog'
import type { SectionNamePayload } from './SectionNameDialog'
import type { TabBusyProbe } from './tabBusyProbe'
import type { TabContext } from './tabContext'
import type { TabCreationCoordinator } from './useTabCreation'
import type { useTabOperations } from './useTabOperations'
import type { WorkspaceStartPoint } from '~/components/workspace/workspaceStartPoint'
import type { AgentProvider } from '~/generated/proto/leapmux/v1/agent_pb'
import type { DialogState, UpdatableDialogState } from '~/hooks/createDialogState'
import type { LinkConfirmState } from '~/hooks/createLinkConfirm'
import type { ChangeBranchMode } from '~/hooks/useGitModeState'
import type { KeyPinDecision } from '~/lib/keyPinStore'
import type { createLayoutStore } from '~/stores/layout.store'
import type { createRepoGitStore } from '~/stores/repoGit.store'
import type { createSectionStore } from '~/stores/section.store'
import type { RepoRef } from '~/stores/tab.helpers'
import type { Tab } from '~/stores/tab.types'
import { createSignal, onCleanup, Show } from 'solid-js'
import { ConfirmDialog } from '~/components/common/ConfirmDialog'
import { KeyPinMismatchDialog } from '~/components/common/KeyPinMismatchDialog'
import { showWarnToast } from '~/components/common/Toast'
import { UntrustedLinkDialog } from '~/components/common/UntrustedLinkDialog'
import { SetGoalDialog } from '~/components/goal/SetGoalDialog'
import { ChangeBranchDialog } from '~/components/workspace/ChangeBranchDialog'
import { DeleteBranchDialog } from '~/components/workspace/DeleteBranchDialog'
import { NewWorkspaceDialog } from '~/components/workspace/NewWorkspaceDialog'
import { BusyTabCloseDialog } from './BusyTabCloseDialog'
import { LastTabCloseDialog } from './LastTabCloseDialog'
import { NewAgentDialog } from './NewAgentDialog'
import { NewTerminalDialog } from './NewTerminalDialog'
import { placeWorkspaceInSection } from './placeWorkspaceInSection'
import { SectionNameDialog } from './SectionNameDialog'
import { useSectionOperations } from './useSectionOperations'
import { isDefinitiveTabCreationError } from './useTabCreation'

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
 * Open-time payload for the "Empty archive" confirm.
 *
 * The COUNT, not a workspace id: this is one prompt for the whole set. The
 * per-workspace confirm is suppressed deliberately -- `confirmDeleteWs` is a
 * single-slot dialog state, so N concurrent opens would drop N-1 resolvers and
 * leave those awaits pending forever.
 */
export interface EmptyArchiveConfirmPayload {
  count: number
  resolve: (confirmed: boolean) => void
}

/** Open-time payload for the "Delete section" confirm. */
export interface SectionConfirmPayload {
  sectionId: string
  sectionName: string
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
  confirmEmptyArchive: DialogState<EmptyArchiveConfirmPayload>
  sectionName: DialogState<SectionNamePayload>
  confirmDeleteSection: DialogState<SectionConfirmPayload>
  // The only updatable one: LastTabCloseDialog patches its own payload after
  // a status refresh. That capability is what keeps its <Show> non-keyed.
  lastTabConfirm: UpdatableDialogState<LastTabConfirmState>
  busyTabConfirm: DialogState<BusyTabConfirmState>
  keyPinConfirm: DialogState<KeyPinConfirmState>
  confirmLink: DialogState<LinkConfirmState>
  setGoal: DialogState<SetGoalState>
  changeBranch: DialogState<ChangeBranchState>
  deleteBranch: DialogState<DeleteBranchState>
}

/** What the session-goal editor acts on. */
export interface SetGoalState {
  agentId: string
  /** The objective to start from: the current one when replacing, empty when setting. */
  initialObjective: string
}

interface AppShellDialogsProps {
  creation: TabCreationCoordinator
  dialogs: AppShellDialogStates
  /** Commit an edited session goal. A refusal reaches the dialog's own slot as a throw. */
  onSetGoal: (agentId: string, objective: string) => Promise<void>
  /**
   * Called after a successful Change branch / non-worktree Delete
   * branch with the branch the working directory is now on. The
   * parent updates the repo-keyed git store with the new branch label and
   * refreshes diff stats for the affected repo.
   */
  onBranchChanged?: (repo: RepoRef, newBranch: string) => void
  activeWorkspace: () => { id: string } | null
  /**
   * Whether a NAMED workspace takes mutation. Per id, not per active workspace:
   * a dialog can act on a workspace the user is not looking at, and the guard
   * has to answer for the one the RPC is for.
   */
  isWorkspaceMutatable: (workspaceId: string) => boolean
  getCurrentTabContext: () => TabContext
  tabOps: ReturnType<typeof useTabOperations>
  /**
   * The busy scan, for the delete-branch dialog. That flow closes a whole branch
   * group and deletes its working directory, and it runs below `handleTabClose`
   * -- so it is the one close surface nothing else asks on the user's behalf.
   */
  busyProbe: TabBusyProbe
  layoutStore: ReturnType<typeof createLayoutStore>
  sectionStore: ReturnType<typeof createSectionStore>
  loadWorkspaces: () => Promise<void>
  loadSections: () => Promise<void>
  availableProviders?: AgentProvider[]
  onRefreshProviders?: () => void
  repoGitStore: ReturnType<typeof createRepoGitStore>
}

/** Identifies one mounted dialog even when a caller reuses its payload. */
function createDialogViewIdentity(current: () => unknown) {
  const identity = Symbol('dialog')
  const [alive, setAlive] = createSignal(true)
  onCleanup(() => setAlive(false))
  return () => alive()
    ? identity
    : current()
}

export const AppShellDialogs: Component<AppShellDialogsProps> = (props) => {
  const sectionOps = useSectionOperations({
    // eslint-disable-next-line solid/reactivity -- the store is a stable object, and loadSections a stable callback
    sectionStore: props.sectionStore,
    loadSections: () => props.loadSections(),
  })

  /** Explain destination readiness before a dialog submits creation. */
  const newTabBlockedReason = (workspaceId?: string): string | undefined => {
    const active = props.activeWorkspace()
    if (!active)
      return 'Create a workspace first — a tab lives inside a workspace.'
    // Check the originating workspace even when another workspace is active.
    const target = workspaceId ?? active.id
    // Explain the archive refusal before the client submits creation.
    if (!props.isWorkspaceMutatable(target))
      return 'This workspace is archived. Unarchive it to create tabs.'
    // `firstLeafIdFor` answers for ANY workspace and is null exactly when
    // `placementTileId()` would be empty, which is the state this reason describes.
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
        {(target) => {
          const viewIdentity = createDialogViewIdentity(props.dialogs.newAgent.value)
          let destination: string | undefined
          return (
            <NewAgentDialog
            // `||`, not `??`: there is no valid empty worker id or path, so an
            // empty string is the same "unknown" that `undefined` is, and both
            // have to fall through to the tab context.
              defaultWorkerId={target.workerId || props.getCurrentTabContext().workerId}
              defaultWorkingDir={target.workingDir || props.getCurrentTabContext().workingDir}
              {...(props.availableProviders !== undefined
                ? { availableProviders: props.availableProviders }
                : {})}
              {...(props.onRefreshProviders !== undefined
                ? { onRefreshProviders: props.onRefreshProviders }
                : {})}
              blockedReason={newTabBlockedReason}
              repoGitStore={props.repoGitStore}
              onCreate={(input) => {
                destination ??= props.activeWorkspace()?.id
                return props.creation.openAgent({
                  ...input,
                  workspaceId: destination ?? '',
                  retryKey: 'new-agent-dialog',
                  viewIdentity,
                  onObserved: () => props.dialogs.newAgent.close(),
                  onError: (error) => {
                    if (isDefinitiveTabCreationError(error))
                      destination = undefined
                    input.onError(error)
                  },
                })
              }}
              onClose={() => {
                if (props.dialogs.newAgent.value() === target)
                  props.dialogs.newAgent.close()
              }}
            />
          )
        }}
      </Show>

      <Show when={props.dialogs.newTerminal.value()} keyed>
        {(target) => {
          const viewIdentity = createDialogViewIdentity(props.dialogs.newTerminal.value)
          let destination: string | undefined
          return (
            <NewTerminalDialog
              defaultWorkerId={target.workerId || props.getCurrentTabContext().workerId}
              defaultWorkingDir={target.workingDir || props.getCurrentTabContext().workingDir}
              blockedReason={newTabBlockedReason}
              repoGitStore={props.repoGitStore}
              onCreate={(input) => {
                destination ??= props.activeWorkspace()?.id
                return props.creation.openTerminal({
                  ...input,
                  workspaceId: destination ?? '',
                  retryKey: 'new-terminal-dialog',
                  viewIdentity,
                  onObserved: () => props.dialogs.newTerminal.close(),
                  onError: (error) => {
                    if (isDefinitiveTabCreationError(error))
                      destination = undefined
                    input.onError(error)
                  },
                })
              }}
              onClose={() => {
                if (props.dialogs.newTerminal.value() === target)
                  props.dialogs.newTerminal.close()
              }}
            />
          )
        }}
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
        {(payload) => {
          const viewIdentity = createDialogViewIdentity(props.dialogs.newWorkspace.value)
          return (
            <NewWorkspaceDialog
              repoGitStore={props.repoGitStore}
              startPoint={payload.startPoint ?? { kind: 'directory' }}
              {...(props.availableProviders !== undefined
                ? { availableProviders: props.availableProviders }
                : {})}
              {...(props.onRefreshProviders !== undefined
                ? { onRefreshProviders: props.onRefreshProviders }
                : {})}
              onCreate={input => props.creation.openWorkspace({
                ...input,
                retryKey: 'new-workspace-dialog',
                viewIdentity,
                onCommitted: (publication, isCurrent) => {
                  input.onCommitted?.(publication, isCurrent)
                  placeWorkspaceInSection(
                    {
                      sectionStore: props.sectionStore,
                      loadWorkspaces: props.loadWorkspaces,
                      loadSections: props.loadSections,
                      isCurrent,
                    },
                    publication.workspaceId,
                    payload.targetSectionId ?? null,
                  )
                },
                onObserved: () => props.dialogs.newWorkspace.close(),
              })}
              onClose={() => {
                if (props.dialogs.newWorkspace.value() === payload)
                  props.dialogs.newWorkspace.close()
              }}
            />
          )
        }}
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

      <Show when={props.dialogs.confirmEmptyArchive.value()} keyed>
        {state => (
          <ConfirmDialog
            title="Empty archive"
            confirmLabel="Delete all"
            danger
            onConfirm={() => {
              state.resolve(true)
              props.dialogs.confirmEmptyArchive.close()
            }}
            onCancel={() => {
              state.resolve(false)
              props.dialogs.confirmEmptyArchive.close()
            }}
          >
            {/* The count, because this is the one prompt for the whole set --
                and the "not all at once" sentence, because there is no
                transaction: each workspace is its own RPC, so a failure part
                way through leaves the archive partly emptied. */}
            <p>
              {`Delete ${state.count} archived workspace${state.count === 1
                ? ''
                : 's'}? This cannot be undone, and a failure part way through leaves the rest in the archive.`}
            </p>
          </ConfirmDialog>
        )}
      </Show>

      <Show when={props.dialogs.confirmDeleteSection.value()} keyed>
        {state => (
          <ConfirmDialog
            title="Delete section"
            confirmLabel="Delete"
            danger
            onConfirm={() => {
              props.dialogs.confirmDeleteSection.close()
              // The toast lives HERE, not in the operation. The confirm is
              // already dismissed by the time the RPC settles, so this call
              // site owns the only surface left to report a failure on -- and
              // `useSectionOperations` keeps one policy for all three.
              sectionOps.deleteSection(state.sectionId)
                .catch(err => showWarnToast('Failed to delete section', err))
            }}
            onCancel={() => props.dialogs.confirmDeleteSection.close()}
          >
            {/* What the hub actually does inside the delete transaction, so the
                sentence cannot drift from the behaviour. */}
            <p>{`Delete "${state.sectionName}"? Its workspaces move to In progress.`}</p>
          </ConfirmDialog>
        )}
      </Show>

      <Show when={props.dialogs.sectionName.value()} keyed>
        {payload => (
          <SectionNameDialog
            payload={payload}
            onSubmit={name => payload.mode === 'create'
              ? sectionOps.createSection(name, payload.sidebar)
              : sectionOps.renameSection(payload.sectionId, name)}
            onClose={() => props.dialogs.sectionName.close()}
          />
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

      <Show when={props.dialogs.setGoal.value()} keyed>
        {state => (
          <SetGoalDialog
            initialObjective={state.initialObjective}
            onSubmit={objective => props.onSetGoal(state.agentId, objective)}
            onClose={() => props.dialogs.setGoal.close()}
          />
        )}
      </Show>

      {/* Keyed: this payload is never patched in place, unlike lastTabConfirm.
          Raised only for a tab the worktree prompt does NOT cover, so one click
          never answers two dialogs. */}
      <Show when={props.dialogs.busyTabConfirm.value()} keyed>
        {state => (
          <BusyTabCloseDialog
            state={state}
            onDismiss={() => props.dialogs.busyTabConfirm.close()}
          />
        )}
      </Show>

      <Show when={props.dialogs.confirmLink.value()} keyed>
        {state => (
          <UntrustedLinkDialog
            request={state.request}
            onResolve={(approved) => {
              state.resolve(approved)
              props.dialogs.confirmLink.close()
            }}
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
        {(state) => {
          const viewIdentity = createDialogViewIdentity(props.dialogs.changeBranch.value)
          return (
            <ChangeBranchDialog
              workerId={state.workerId}
              gitToplevel={state.gitToplevel}
              branchName={state.branchName}
              isWorktree={state.isWorktree}
              initialMode={state.initialMode}
              {...(props.availableProviders !== undefined
                ? { availableProviders: props.availableProviders }
                : {})}
              {...(props.onRefreshProviders !== undefined
                ? { onRefreshProviders: props.onRefreshProviders }
                : {})}
              onBranchChanged={newBranch => props.onBranchChanged?.(state, newBranch)}
              // Capture the branch row's workspace even when another workspace is active.
              blockedReason={() => newTabBlockedReason(state.workspaceId)}
              createAgent={input => props.creation.openAgent({
                ...input,
                workspaceId: state.workspaceId,
                retryKey: 'change-branch-agent',
                viewIdentity,
                onObserved: () => props.dialogs.changeBranch.close(),
              })}
              createTerminal={input => props.creation.openTerminal({
                ...input,
                workspaceId: state.workspaceId,
                retryKey: 'change-branch-terminal',
                viewIdentity,
                onObserved: () => props.dialogs.changeBranch.close(),
              })}
              onClose={() => {
                if (props.dialogs.changeBranch.value() === state)
                  props.dialogs.changeBranch.close()
              }}
            />
          )
        }}
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
            probeBusy={props.busyProbe.probeMany}
            onBranchChanged={newBranch => props.onBranchChanged?.(state, newBranch)}
            onClose={() => props.dialogs.deleteBranch.close()}
          />
        )}
      </Show>
    </>
  )
}
