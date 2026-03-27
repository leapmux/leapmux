import type { Component } from 'solid-js'
import type { useAgentOperations } from './useAgentOperations'
import type { AgentProvider } from '~/generated/leapmux/v1/agent_pb'
import type { KeyPinDecision } from '~/lib/channel'
import type { createAgentStore } from '~/stores/agent.store'
import type { createLayoutStore } from '~/stores/layout.store'
import type { createTabStore } from '~/stores/tab.store'
import type { createTerminalStore } from '~/stores/terminal.store'
import type { createWorkspaceStore } from '~/stores/workspace.store'
import { onMount, Show } from 'solid-js'
import { sectionClient } from '~/api/clients'
import { ConfirmButton } from '~/components/common/ConfirmButton'
import { ConfirmDialog } from '~/components/common/ConfirmDialog'
import { KeyPinMismatchDialog } from '~/components/common/KeyPinMismatchDialog'
import { DiffStatsBadge } from '~/components/tree/gitStatusUtils'
import { NewWorkspaceDialog } from '~/components/workspace/NewWorkspaceDialog'
import { LastTabCloseTarget } from '~/generated/leapmux/v1/git_pb'
import { TabType } from '~/generated/leapmux/v1/workspace_pb'
import { dialogStandard } from '~/styles/shared.css'
import { NewAgentDialog } from './NewAgentDialog'
import { NewTerminalDialog } from './NewTerminalDialog'
import { ResumeSessionDialog } from './ResumeSessionDialog'
import { nextTabNumber } from './useAgentOperations'

interface LastTabConfirmState {
  target: LastTabCloseTarget
  repoRoot: string
  worktreePath: string
  worktreeId: string
  branchName: string
  diffAdded: number
  diffDeleted: number
  diffUntracked: number
  unpushedCommitCount: number
  hasUncommittedChanges: boolean
  upstreamExists: boolean
  remoteBranchMissing: boolean
  originExists: boolean
  canPush: boolean
  pushLabel: string
  resolve: (choice: 'cancel' | 'push' | 'schedule-delete' | 'close-anyway') => void
}

export interface KeyPinConfirmState {
  workerId: string
  expectedFingerprint: string
  actualFingerprint: string
  resolve: (decision: KeyPinDecision) => void
}

interface AppShellDialogsProps {
  showResumeDialog: boolean
  setShowResumeDialog: (v: boolean) => void
  showNewAgentDialog: boolean
  setShowNewAgentDialog: (v: boolean) => void
  showNewTerminalDialog: boolean
  setShowNewTerminalDialog: (v: boolean) => void
  showNewWorkspace: boolean
  setShowNewWorkspace: (v: boolean) => void
  preselectedWorkerId: string | undefined
  setPreselectedWorkerId: (v: string | undefined) => void
  newWorkspaceTargetSectionId: string | null
  setNewWorkspaceTargetSectionId: (v: string | null) => void
  confirmDeleteWs: { workspaceId: string, resolve: (confirmed: boolean) => void } | null
  setConfirmDeleteWs: (v: { workspaceId: string, resolve: (confirmed: boolean) => void } | null) => void
  confirmArchiveWs: { workspaceId: string, resolve: (confirmed: boolean) => void } | null
  setConfirmArchiveWs: (v: { workspaceId: string, resolve: (confirmed: boolean) => void } | null) => void
  lastTabConfirm: LastTabConfirmState | null
  setLastTabConfirm: (v: LastTabConfirmState | null) => void
  keyPinConfirm: KeyPinConfirmState | null
  setKeyPinConfirm: (v: KeyPinConfirmState | null) => void
  activeWorkspace: () => { id: string } | null
  getCurrentTabContext: () => { workerId: string, workingDir: string, homeDir: string }
  agentOps: ReturnType<typeof useAgentOperations>
  agentStore: ReturnType<typeof createAgentStore>
  tabStore: ReturnType<typeof createTabStore>
  terminalStore: ReturnType<typeof createTerminalStore>
  layoutStore: ReturnType<typeof createLayoutStore>
  workspaceStore: ReturnType<typeof createWorkspaceStore>
  persistLayout: () => void
  focusEditor: () => void
  orgSlug: string
  loadWorkspaces: () => Promise<void>
  navigate: (path: string) => void
  availableProviders?: AgentProvider[]
  onRefreshProviders?: () => void
}

export const AppShellDialogs: Component<AppShellDialogsProps> = (props) => {
  return (
    <>
      <Show when={props.showResumeDialog}>
        <ResumeSessionDialog
          defaultWorkerId={props.getCurrentTabContext().workerId}
          onResume={props.agentOps.handleResumeAgent}
          onClose={() => props.setShowResumeDialog(false)}
        />
      </Show>

      <Show when={props.showNewAgentDialog}>
        <NewAgentDialog
          workspaceId={props.activeWorkspace()?.id ?? ''}
          defaultWorkerId={props.getCurrentTabContext().workerId}
          defaultWorkingDir={props.getCurrentTabContext().workingDir}
          defaultTitle={`Agent ${nextTabNumber(props.tabStore.state.tabs, TabType.AGENT, 'Agent')}`}
          availableProviders={props.availableProviders}
          onRefreshProviders={props.onRefreshProviders}
          onCreated={(agent) => {
            props.setShowNewAgentDialog(false)
            const tileId = props.layoutStore.focusedTileId()
            props.agentStore.addAgent(agent)
            props.tabStore.addTab({
              type: TabType.AGENT,
              id: agent.id,
              title: agent.title || undefined,
              tileId,
              workerId: agent.workerId,
              workingDir: agent.workingDir,
              agentProvider: agent.agentProvider,
            })
            props.tabStore.setActiveTabForTile(tileId, TabType.AGENT, agent.id)
            props.persistLayout()
            requestAnimationFrame(() => props.focusEditor())
          }}
          onClose={() => props.setShowNewAgentDialog(false)}
        />
      </Show>

      <Show when={props.showNewTerminalDialog}>
        <NewTerminalDialog
          workspaceId={props.activeWorkspace()?.id ?? ''}
          defaultWorkerId={props.getCurrentTabContext().workerId}
          defaultWorkingDir={props.getCurrentTabContext().workingDir}
          onCreated={(terminalId, workerId, workingDir) => {
            props.setShowNewTerminalDialog(false)
            const ws = props.activeWorkspace()
            if (!ws)
              return
            const title = `Terminal ${nextTabNumber(props.tabStore.state.tabs, TabType.TERMINAL, 'Terminal')}`
            const tileId = props.layoutStore.focusedTileId()
            props.terminalStore.addTerminal({ id: terminalId, workspaceId: ws.id, workerId, workingDir })
            props.tabStore.addTab({ type: TabType.TERMINAL, id: terminalId, title, tileId, workerId, workingDir })
            props.tabStore.setActiveTabForTile(tileId, TabType.TERMINAL, terminalId)
            props.persistLayout()
          }}
          onClose={() => props.setShowNewTerminalDialog(false)}
        />
      </Show>

      <Show when={props.showNewWorkspace}>
        <NewWorkspaceDialog
          preselectedWorkerId={props.preselectedWorkerId}
          availableProviders={props.availableProviders}
          onRefreshProviders={props.onRefreshProviders}
          onCreated={(ws, _wid) => {
            props.workspaceStore.addWorkspace(ws)
            props.setShowNewWorkspace(false)
            props.setPreselectedWorkerId(undefined)
            const targetSectionId = props.newWorkspaceTargetSectionId
            const refreshWorkspaces = props.loadWorkspaces
            const clearTargetSection = props.setNewWorkspaceTargetSectionId
            if (targetSectionId) {
              sectionClient.moveWorkspace({
                workspaceId: ws.id,
                sectionId: targetSectionId,
                position: 'N',
              }).catch(() => {}).finally(() => {
                clearTargetSection(null)
                refreshWorkspaces()
              })
            }
            else {
              refreshWorkspaces()
            }
            props.navigate(`/o/${props.orgSlug}/workspace/${ws.id}`)
          }}
          onClose={() => {
            props.setShowNewWorkspace(false)
            props.setPreselectedWorkerId(undefined)
            props.setNewWorkspaceTargetSectionId(null)
          }}
        />
      </Show>

      <Show when={props.confirmDeleteWs}>
        {state => (
          <ConfirmDialog
            title="Delete Workspace"
            confirmLabel="Delete"
            danger
            onConfirm={() => {
              state().resolve(true)
              props.setConfirmDeleteWs(null)
            }}
            onCancel={() => {
              state().resolve(false)
              props.setConfirmDeleteWs(null)
            }}
          >
            <p>Are you sure you want to delete this workspace? This cannot be undone.</p>
          </ConfirmDialog>
        )}
      </Show>

      <Show when={props.confirmArchiveWs}>
        {state => (
          <ConfirmDialog
            title="Archive Workspace"
            confirmLabel="Archive"
            onConfirm={() => {
              state().resolve(true)
              props.setConfirmArchiveWs(null)
            }}
            onCancel={() => {
              state().resolve(false)
              props.setConfirmArchiveWs(null)
            }}
          >
            <p>Are you sure you want to archive this workspace? All active agents and terminals will be stopped.</p>
          </ConfirmDialog>
        )}
      </Show>

      <Show when={props.lastTabConfirm}>
        {(confirm) => {
          let dlgRef!: HTMLDialogElement
          onMount(() => dlgRef.showModal())
          const handleCancel = () => {
            confirm().resolve('cancel')
            props.setLastTabConfirm(null)
          }
          const handlePush = () => {
            confirm().resolve('push')
            props.setLastTabConfirm(null)
          }
          const handleScheduleDelete = () => {
            confirm().resolve('schedule-delete')
            props.setLastTabConfirm(null)
          }
          const handleCloseAnyway = () => {
            confirm().resolve('close-anyway')
            props.setLastTabConfirm(null)
          }
          return (
            <dialog ref={dlgRef} class={dialogStandard} onClose={handleCancel}>
              <header><h2>Close Last Tab</h2></header>
              <section>
                <p>
                  <Show
                    when={confirm().target === LastTabCloseTarget.WORKTREE}
                    fallback={(
                      <>
                        You are closing the last non-worktree tab for branch
                        {' '}
                        <code>{confirm().branchName}</code>
                        .
                      </>
                    )}
                  >
                    <>
                      You are closing the last tab for worktree
                      {' '}
                      <code>{confirm().worktreePath}</code>
                      .
                    </>
                  </Show>
                </p>
                <p>
                  Branch:
                  {' '}
                  <code>{confirm().branchName}</code>
                </p>
                <Show when={confirm().hasUncommittedChanges}>
                  <p>
                    Uncommitted changes:
                    {' '}
                    <DiffStatsBadge added={confirm().diffAdded} deleted={confirm().diffDeleted} untracked={confirm().diffUntracked} />
                  </p>
                </Show>
                <Show when={confirm().unpushedCommitCount > 0}>
                  <p>
                    {confirm().unpushedCommitCount}
                    {' '}
                    commit
                    {confirm().unpushedCommitCount === 1 ? '' : 's'}
                    {' '}
                    not pushed.
                  </p>
                </Show>
                <Show when={confirm().remoteBranchMissing}>
                  <p>Remote branch does not exist.</p>
                </Show>
              </section>
              <footer>
                <button type="button" class="outline" onClick={handleCancel}>
                  Cancel
                </button>
                <Show when={confirm().canPush}>
                  <button type="button" onClick={handlePush}>
                    {confirm().pushLabel}
                  </button>
                </Show>
                <Show when={confirm().target === LastTabCloseTarget.WORKTREE}>
                  <ConfirmButton data-variant="danger" onClick={handleScheduleDelete}>
                    Schedule worktree deletion
                  </ConfirmButton>
                </Show>
                <ConfirmButton data-variant="danger" onClick={handleCloseAnyway}>
                  Close anyway
                </ConfirmButton>
              </footer>
            </dialog>
          )
        }}
      </Show>

      <Show when={props.keyPinConfirm}>
        {state => (
          <KeyPinMismatchDialog
            workerId={state().workerId}
            expectedFingerprint={state().expectedFingerprint}
            actualFingerprint={state().actualFingerprint}
            resolve={(decision) => {
              state().resolve(decision)
              props.setKeyPinConfirm(null)
            }}
          />
        )}
      </Show>
    </>
  )
}
