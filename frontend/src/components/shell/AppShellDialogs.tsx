import type { Component } from 'solid-js'
import type { useAgentOperations } from './useAgentOperations'
import type { createAgentStore } from '~/stores/agent.store'
import type { createLayoutStore } from '~/stores/layout.store'
import type { createTabStore } from '~/stores/tab.store'
import type { createTerminalStore } from '~/stores/terminal.store'
import type { createWorkspaceStore } from '~/stores/workspace.store'
import { onMount, Show } from 'solid-js'
import { sectionClient } from '~/api/clients'
import { ConfirmButton } from '~/components/common/ConfirmButton'
import { ConfirmDialog } from '~/components/common/ConfirmDialog'
import { NewWorkspaceDialog } from '~/components/workspace/NewWorkspaceDialog'
import { TabType } from '~/generated/leapmux/v1/workspace_pb'
import { dialogStandard } from '~/styles/shared.css'
import { NewAgentDialog } from './NewAgentDialog'
import { NewTerminalDialog } from './NewTerminalDialog'
import { ResumeSessionDialog } from './ResumeSessionDialog'
import { nextTabNumber } from './useAgentOperations'

interface WorktreeConfirmState {
  path: string
  id: string
  branchName: string
  resolve: (choice: 'cancel' | 'keep' | 'remove') => void
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
  worktreeConfirm: WorktreeConfirmState | null
  setWorktreeConfirm: (v: WorktreeConfirmState | null) => void
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
          onCreated={(ws) => {
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

      <Show when={props.worktreeConfirm}>
        {(confirm) => {
          let dlgRef!: HTMLDialogElement
          onMount(() => dlgRef.showModal())
          const handleCancel = () => {
            confirm().resolve('cancel')
            props.setWorktreeConfirm(null)
          }
          const handleKeep = () => {
            confirm().resolve('keep')
            props.setWorktreeConfirm(null)
          }
          const handleRemove = () => {
            confirm().resolve('remove')
            props.setWorktreeConfirm(null)
          }
          return (
            <dialog ref={dlgRef} class={dialogStandard} onClose={handleCancel}>
              <header><h2>Dirty Worktree</h2></header>
              <section>
                <p>The worktree has uncommitted changes or unpushed commits:</p>
                <p><code>{confirm().path}</code></p>
                <p>
                  Both the worktree and its branch
                  <code>{confirm().branchName}</code>
                  {' '}
                  will be deleted. Keep them on disk, or cancel?
                </p>
              </section>
              <footer>
                <button type="button" class="outline" onClick={handleCancel}>
                  Cancel
                </button>
                <button type="button" onClick={handleKeep}>
                  Keep
                </button>
                <ConfirmButton data-variant="danger" onClick={handleRemove}>
                  Remove
                </ConfirmButton>
              </footer>
            </dialog>
          )
        }}
      </Show>
    </>
  )
}
