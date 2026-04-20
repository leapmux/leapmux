import type { Component } from 'solid-js'
import type { useAgentOperations } from './useAgentOperations'
import type { AgentProvider } from '~/generated/leapmux/v1/agent_pb'
import type { InspectLastTabCloseResponse } from '~/generated/leapmux/v1/git_pb'
import type { KeyPinDecision } from '~/lib/channel'
import type { createAgentStore } from '~/stores/agent.store'
import type { createLayoutStore } from '~/stores/layout.store'
import type { createTabStore } from '~/stores/tab.store'
import type { createWorkspaceStore } from '~/stores/workspace.store'
import LoaderCircle from 'lucide-solid/icons/loader-circle'
import { createSignal, Show } from 'solid-js'
import { sectionClient } from '~/api/clients'
import { ConfirmButton } from '~/components/common/ConfirmButton'
import { ConfirmDialog } from '~/components/common/ConfirmDialog'
import { Dialog } from '~/components/common/Dialog'
import { Icon } from '~/components/common/Icon'
import { KeyPinMismatchDialog } from '~/components/common/KeyPinMismatchDialog'
import { showWarnToast } from '~/components/common/Toast'
import { DiffStatsBadge } from '~/components/tree/gitStatusUtils'
import { NewWorkspaceDialog } from '~/components/workspace/NewWorkspaceDialog'
import { LastTabCloseTarget } from '~/generated/leapmux/v1/git_pb'
import { TabType } from '~/generated/leapmux/v1/workspace_pb'
import { pluralize } from '~/lib/plural'
import { diffStatsFromTabFields } from '~/stores/gitFileStatus.store'
import { spinner } from '~/styles/animations.css'
import { NewAgentDialog } from './NewAgentDialog'
import { NewTerminalDialog } from './NewTerminalDialog'
import { nextTabNumber } from './useAgentOperations'

type LastTabCloseChoice = 'cancel' | 'schedule-delete' | 'close-anyway'

interface LastTabConfirmState extends InspectLastTabCloseResponse {
  resolve: (choice: LastTabCloseChoice) => void
  onPush: () => Promise<void>
}

export interface KeyPinConfirmState {
  workerId: string
  expectedFingerprint: string
  actualFingerprint: string
  resolve: (decision: KeyPinDecision) => void
}

interface AppShellDialogsProps {
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
            const afterKey = props.tabStore.getActiveTabKeyForTile(tileId)
            props.agentStore.addAgent(agent)
            props.tabStore.addTab({
              type: TabType.AGENT,
              id: agent.id,
              title: agent.title || undefined,
              tileId,
              workerId: agent.workerId,
              workingDir: agent.workingDir,
              agentProvider: agent.agentProvider,
            }, { afterKey })
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
            const afterKey = props.tabStore.getActiveTabKeyForTile(tileId)
            props.tabStore.addTab({ type: TabType.TERMINAL, id: terminalId, title, tileId, workerId, workingDir, status: 'running' }, { afterKey })
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
          onCreated={(workspaceId, _wid) => {
            props.setShowNewWorkspace(false)
            props.setPreselectedWorkerId(undefined)
            const targetSectionId = props.newWorkspaceTargetSectionId
            const refreshWorkspaces = props.loadWorkspaces
            const clearTargetSection = props.setNewWorkspaceTargetSectionId
            if (targetSectionId) {
              sectionClient.moveWorkspace({
                workspaceId,
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
            props.navigate(`/o/${props.orgSlug}/workspace/${workspaceId}`)
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
          const handleCancel = () => {
            confirm().resolve('cancel')
            props.setLastTabConfirm(null)
          }
          const [pushing, setPushing] = createSignal(false)
          const handlePush = async () => {
            setPushing(true)
            try {
              await confirm().onPush()
            }
            catch (err) {
              showWarnToast('Failed to push branch', err)
            }
            finally {
              setPushing(false)
            }
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
            <Dialog title="Close Last Tab" onClose={handleCancel}>
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
                    You are closing the last tab for worktree
                    {' '}
                    <code>{confirm().worktreePath}</code>
                    .
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
                    <DiffStatsBadge stats={diffStatsFromTabFields(confirm())} />
                  </p>
                </Show>
                <Show when={confirm().unpushedCommitCount > 0}>
                  <p>
                    {pluralize(confirm().unpushedCommitCount, 'commit')}
                    {' '}
                    not pushed.
                  </p>
                </Show>
                <Show when={confirm().remoteBranchMissing || (!confirm().upstreamExists && confirm().canPush)}>
                  <p>Branch not pushed to remote.</p>
                </Show>
                <Show when={!confirm().hasUncommittedChanges && confirm().unpushedCommitCount === 0 && !confirm().remoteBranchMissing && confirm().upstreamExists}>
                  <p>No uncommitted changes or unpushed commits.</p>
                </Show>
              </section>
              <footer>
                <button type="button" class="outline" onClick={handleCancel}>
                  Cancel
                </button>
                <Show when={confirm().canPush}>
                  <button type="button" onClick={handlePush} disabled={pushing()}>
                    {confirm().hasUncommittedChanges ? 'Commit and Push' : 'Push'}
                    <Show when={pushing()}><Icon icon={LoaderCircle} size="sm" class={spinner} /></Show>
                  </button>
                </Show>
                <Show when={confirm().target === LastTabCloseTarget.WORKTREE}>
                  <ConfirmButton data-variant="danger" onClick={handleScheduleDelete}>
                    Delete
                  </ConfirmButton>
                </Show>
                <ConfirmButton data-variant="danger" onClick={handleCloseAnyway}>
                  Close anyway
                </ConfirmButton>
              </footer>
            </Dialog>
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
