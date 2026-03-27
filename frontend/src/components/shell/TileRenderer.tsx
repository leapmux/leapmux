import type { Component } from 'solid-js'
import type { useAgentOperations } from './useAgentOperations'
import type { useTerminalOperations } from './useTerminalOperations'
import type { FileAttachment } from '~/components/chat/attachments'
import type { createLoadingSignal } from '~/hooks/createLoadingSignal'
import type { createAgentStore } from '~/stores/agent.store'
import type { createAgentSessionStore } from '~/stores/agentSession.store'
import type { createChatStore } from '~/stores/chat.store'
import type { createControlStore } from '~/stores/control.store'
import type { createGitFileStatusStore } from '~/stores/gitFileStatus.store'
import type { createLayoutStore } from '~/stores/layout.store'
import type { createTabStore, Tab } from '~/stores/tab.store'
import type { createTerminalStore } from '~/stores/terminal.store'
import Bot from 'lucide-solid/icons/bot'
import Terminal from 'lucide-solid/icons/terminal'
import { createEffect, createMemo, For, onCleanup, Show } from 'solid-js'
import * as workerRpc from '~/api/workerRpc'
import { AgentEditorPanel } from '~/components/chat/AgentEditorPanel'
import { ChatView } from '~/components/chat/ChatView'
import { relativizePath } from '~/components/chat/messageUtils'
import { Icon } from '~/components/common/Icon'
import { showWarnToast } from '~/components/common/Toast'
import { FileViewer } from '~/components/fileviewer/FileViewer'
import { TerminalView } from '~/components/terminal/TerminalView'
import { ContentCompression, MessageRole } from '~/generated/leapmux/v1/agent_pb'
import { GitFileStatusCode } from '~/generated/leapmux/v1/common_pb'
import { TabType } from '~/generated/leapmux/v1/workspace_pb'
import { formatFileMention, formatFileQuote } from '~/lib/quoteUtils'
import { MAX_LOADED_CHAT_MESSAGES } from '~/stores/chat.store'
import { appendText, insertIntoMruAgentEditor } from '~/stores/editorRef.store'
import { tabKey } from '~/stores/tab.store'
import { shouldShowThinkingIndicator } from '~/utils/agentState'
import * as styles from './AppShell.css'
import { TabBar } from './TabBar'
import { Tile } from './Tile'

interface TileRendererOpts {
  tabStore: ReturnType<typeof createTabStore>
  agentStore: ReturnType<typeof createAgentStore>
  chatStore: ReturnType<typeof createChatStore>
  terminalStore: ReturnType<typeof createTerminalStore>
  controlStore: ReturnType<typeof createControlStore>
  layoutStore: ReturnType<typeof createLayoutStore>
  agentSessionStore: ReturnType<typeof createAgentSessionStore>
  settingsLoading: ReturnType<typeof createLoadingSignal>
  agentOps: ReturnType<typeof useAgentOperations>
  termOps: ReturnType<typeof useTerminalOperations>
  hasMultipleTiles: () => boolean
  isActiveWorkspaceMutatable: () => boolean
  isActiveWorkspaceArchived: () => boolean
  activeWorkspace: () => { id: string } | null
  getCurrentTabContext: () => { workerId: string, workingDir: string, homeDir: string }
  getMruAgentContext: () => { workingDir: string, homeDir: string }
  handleTabSelect: (tab: Tab) => void
  handleTabClose: (tab: Tab) => Promise<void>
  setIsTabEditing: (fn: () => boolean) => void
  persistLayout: () => void
  closingTabKeys: () => Set<string>
  newAgentLoading: () => boolean
  newTerminalLoading: () => boolean
  newShellLoading: () => boolean
  isMobile: () => boolean
  toggleLeftSidebar: () => void
  toggleRightSidebar: () => void
  setShowResumeDialog: (v: boolean) => void
  setShowNewAgentDialog: (v: boolean) => void
  setShowNewTerminalDialog: (v: boolean) => void
  focusEditorRef: { current: (() => void) | undefined }
  getScrollStateRef: { current: (() => { distFromBottom: number, atBottom: boolean } | undefined) | undefined }
  forceScrollToBottomRef: { current: (() => void) | undefined }
  gitFileStatusStore?: ReturnType<typeof createGitFileStatusStore>
  // Floating window support
  isFloatingWindowTile?: (tileId: string) => boolean
  onDetachTab?: (tab: Tab) => void
}

export function createTileRenderer(opts: TileRendererOpts) {
  const {
    tabStore,
    agentStore,
    chatStore,
    terminalStore,
    controlStore,
    layoutStore,
    agentSessionStore,
    settingsLoading,
    agentOps,
    termOps,
    hasMultipleTiles,
    isActiveWorkspaceMutatable,
    isActiveWorkspaceArchived,
    activeWorkspace,
    getCurrentTabContext,
    getMruAgentContext,
    handleTabSelect,
    handleTabClose,
    setIsTabEditing,
    persistLayout,
    closingTabKeys,
    newAgentLoading,
    newTerminalLoading,
    newShellLoading,
    isMobile,
    toggleLeftSidebar,
    toggleRightSidebar,
    setShowResumeDialog,
    setShowNewAgentDialog,
    setShowNewTerminalDialog,
    focusEditorRef,
    getScrollStateRef,
    forceScrollToBottomRef,
  } = opts

  const getActiveTabForTile = (tileId: string): Tab | null => {
    const key = tabStore.getActiveTabKeyForTile(tileId)
    if (!key)
      return null
    return tabStore.state.tabs.find(t => tabKey(t) === key) ?? null
  }

  const agentThinking = (agentId: string) => shouldShowThinkingIndicator(
    agentStore.state.agents.find(a => a.id === agentId),
    agentSessionStore.getInfo(agentId),
    chatStore.getMessages(agentId),
    chatStore.state.streamingText[agentId],
    controlStore.getRequests(agentId).length,
  )

  const createTabBarForTile = (tileId: string) => (
    <TabBar
      tileId={tileId}
      tabs={tabStore.getTabsForTile(tileId)}
      activeTabKey={tabStore.getActiveTabKeyForTile(tileId)}
      showAddButton={isActiveWorkspaceMutatable()}
      readOnly={isActiveWorkspaceArchived()}
      onSelect={(tab) => {
        layoutStore.setFocusedTile(tileId)
        handleTabSelect(tab)
        tabStore.setActiveTabForTile(tileId, tab.type, tab.id)
      }}
      onClose={handleTabClose}
      onRename={(tab, title) => {
        tabStore.updateTabTitle(tab.type, tab.id, title)
        if (tab.type === TabType.AGENT) {
          const renameWorkerId = agentStore.state.agents.find(a => a.id === tab.id)?.workerId ?? ''
          workerRpc.renameAgent(renameWorkerId, { agentId: tab.id, title }).catch((err) => {
            showWarnToast('Failed to rename agent', err)
          })
        }
      }}
      hasActiveTabContext={!!getCurrentTabContext().workerId}
      isEditingRef={(fn) => { setIsTabEditing(fn) }}
      availableProviders={agentOps.availableProviders()}
      onNewAgent={agentOps.handleOpenAgent}
      onNewTerminal={termOps.handleOpenTerminal}
      availableShells={termOps.availableShells()}
      defaultShell={termOps.defaultShell()}
      onNewTerminalWithShell={termOps.handleOpenTerminalWithShell}
      onResumeSession={() => setShowResumeDialog(true)}
      onNewAgentAdvanced={() => setShowNewAgentDialog(true)}
      onNewTerminalAdvanced={() => setShowNewTerminalDialog(true)}
      newAgentLoading={newAgentLoading()}
      newTerminalLoading={newTerminalLoading()}
      newShellLoading={newShellLoading()}
      closingTabKeys={closingTabKeys()}
      isMobile={isMobile()}
      onToggleLeftSidebar={toggleLeftSidebar}
      onToggleRightSidebar={toggleRightSidebar}
      tileActions={{
        canSplit: layoutStore.canSplitTile(tileId),
        canClose: hasMultipleTiles(),
        onSplitHorizontal: () => {
          layoutStore.splitTileHorizontal(tileId)
          persistLayout()
        },
        onSplitVertical: () => {
          layoutStore.splitTileVertical(tileId)
          persistLayout()
        },
        onClose: () => {
          layoutStore.closeTile(tileId)
          persistLayout()
        },
      }}
    />
  )

  const tabBarElement = () => createTabBarForTile(layoutStore.focusedTileId())

  const renderTileContent = (tileId: string) => {
    const tab = () => getActiveTabForTile(tileId)
    const agentTab = () => {
      const t = tab()
      return t?.type === TabType.AGENT ? t : null
    }
    const terminalTab = () => {
      const t = tab()
      return t?.type === TabType.TERMINAL ? t : null
    }
    const fileTab = () => {
      const t = tab()
      return t?.type === TabType.FILE ? t : null
    }
    const tileAgentTabs = () => tabStore.getTabsForTile(tileId).filter(t => t.type === TabType.AGENT)
    const tileFileTabs = () => tabStore.getTabsForTile(tileId).filter(t => t.type === TabType.FILE)
    const agentScrollStates = new Map<string, () => { distFromBottom: number, atBottom: boolean } | undefined>()
    const agentScrollToBottoms = new Map<string, () => void>()
    createEffect(() => {
      const activeId = agentTab()?.id
      if (activeId) {
        getScrollStateRef.current = agentScrollStates.get(activeId)
        forceScrollToBottomRef.current = agentScrollToBottoms.get(activeId)
      }
    })
    const tileTerminalIds = () => new Set(
      tabStore.getTabsForTile(tileId)
        .filter(t => t.type === TabType.TERMINAL)
        .map(t => t.id),
    )
    const hasTerminals = () => tileTerminalIds().size > 0
    const tileTerminals = () => {
      const ids = tileTerminalIds()
      return terminalStore.state.terminals.filter(t => ids.has(t.id))
    }

    return (
      <>
        <For each={tileAgentTabs()}>
          {(at) => {
            const agentId = at.id
            const agent = () => agentStore.state.agents.find(a => a.id === agentId)
            onCleanup(() => {
              agentScrollStates.delete(agentId)
              agentScrollToBottoms.delete(agentId)
            })
            return (
              <div class={styles.centerContent} classList={{ [styles.layoutHidden]: agentTab()?.id !== at.id }}>
                <Show
                  when={agent()}
                  fallback={<div class={styles.placeholder}>Agent not found.</div>}
                >
                  <ChatView
                    messages={chatStore.getMessages(agentId)}
                    messageVersion={chatStore.getMessageVersion(agentId)}
                    streamingText={chatStore.state.streamingText[agentId] ?? ''}
                    streamingType={agentSessionStore.getInfo(agentId).streamingType}
                    agentWorking={agentThinking(agentId)}
                    messageErrors={chatStore.state.messageErrors}
                    onRetryMessage={messageId => agentOps.handleRetryMessage(agentId, messageId)}
                    onDeleteMessage={messageId => agentOps.handleDeleteMessage(agentId, messageId)}
                    workingDir={agentStore.state.agents.find(a => a.id === agentId)?.workingDir}
                    homeDir={agentStore.state.agents.find(a => a.id === agentId)?.homeDir}
                    hasOlderMessages={chatStore.hasOlderMessages(agentId)}
                    fetchingOlder={chatStore.isFetchingOlder(agentId)}
                    onLoadOlderMessages={() => chatStore.loadOlderMessages(agentStore.state.agents.find(a => a.id === agentId)?.workerId ?? '', agentId)}
                    onTrimOldMessages={() => chatStore.trimOldMessages(agentId, MAX_LOADED_CHAT_MESSAGES)}
                    savedViewportScroll={chatStore.getSavedViewportScroll(agentId)}
                    onClearSavedViewportScroll={() => chatStore.clearSavedViewportScroll(agentId)}
                    scrollStateRef={(fn) => {
                      agentScrollStates.set(agentId, fn)
                      if (agentTab()?.id === at.id)
                        getScrollStateRef.current = fn
                    }}
                    scrollToBottomRef={(fn) => {
                      agentScrollToBottoms.set(agentId, fn)
                      if (agentTab()?.id === at.id)
                        forceScrollToBottomRef.current = fn
                    }}
                    getMessageBySpanId={spanId => chatStore.getMessageBySpanId(agentId, spanId)}
                    getCommandStreamBySpanId={spanId => chatStore.getCommandStream(agentId, spanId)}
                    onQuote={isActiveWorkspaceArchived()
                      ? undefined
                      : (text) => {
                          appendText(agentId, text)
                          focusEditorRef.current?.()
                        }}
                    onReply={isActiveWorkspaceArchived()
                      ? undefined
                      : (text) => {
                          appendText(agentId, text)
                          focusEditorRef.current?.()
                        }}
                  />
                </Show>
              </div>
            )
          }}
        </For>

        <Show when={hasTerminals()}>
          <div
            class={styles.centerContent}
            classList={{ [styles.layoutHidden]: !terminalTab() }}
          >
            <TerminalView
              terminals={tileTerminals()}
              activeTerminalId={terminalTab()?.id ?? null}
              visible={!!terminalTab()}
              onInput={termOps.handleTerminalInput}
              onResize={termOps.handleTerminalResize}
              onTitleChange={termOps.handleTerminalTitleChange}
              onBell={termOps.handleTerminalBell}
            />
          </div>
        </Show>

        <For each={tileFileTabs()}>
          {(ft) => {
            const fileRelPath = () => {
              const ctx = getMruAgentContext()
              return relativizePath(ft.filePath ?? '', ctx.workingDir, ctx.homeDir)
            }
            return (
              <div class={styles.centerContent} classList={{ [styles.layoutHidden]: fileTab()?.id !== ft.id }}>
                <FileViewer
                  workerId={ft.workerId ?? ''}
                  filePath={ft.filePath ?? ''}
                  displayMode={ft.displayMode}
                  onDisplayModeChange={mode => tabStore.setTabDisplayMode(ft.type, ft.id, mode)}
                  onQuote={isActiveWorkspaceArchived()
                    ? undefined
                    : (text, startLine, endLine) => {
                        if (startLine != null && endLine != null) {
                          insertIntoMruAgentEditor(tabStore, formatFileQuote(fileRelPath(), startLine, endLine, text))
                        }
                      }}
                  onMention={isActiveWorkspaceArchived()
                    ? undefined
                    : () => {
                        insertIntoMruAgentEditor(tabStore, formatFileMention(fileRelPath()), 'inline')
                      }}
                  fileViewMode={ft.fileViewMode}
                  fileDiffBase={ft.fileDiffBase}
                  hasStagedAndUnstaged={(() => {
                    const store = opts.gitFileStatusStore
                    if (!store)
                      return false
                    const entry = store.getFileStatus(ft.filePath ?? '')
                    if (!entry)
                      return false
                    return entry.stagedStatus !== GitFileStatusCode.UNSPECIFIED
                      && entry.unstagedStatus !== GitFileStatusCode.UNSPECIFIED
                  })()}
                  onFileViewModeChange={mode => tabStore.setTabFileViewMode(ft.type, ft.id, mode)}
                  onFileDiffBaseChange={base => tabStore.setTabFileDiffBase(ft.type, ft.id, base)}
                />
              </div>
            )
          }}
        </For>

        <Show when={!tab() && activeWorkspace()}>
          <Show
            when={!isActiveWorkspaceArchived()}
            fallback={(
              <div class={styles.placeholder} data-testid="tile-empty-state">
                This workspace is archived. Unarchive it to create new agents or terminals.
              </div>
            )}
          >
            <Show
              when={!hasMultipleTiles() || layoutStore.focusedTileId() === tileId}
              fallback={(
                <div class={styles.emptyTileHint} data-testid="empty-tile-hint">
                  No tabs in this tile.
                </div>
              )}
            >
              <div class={styles.emptyTileActions} data-testid="empty-tile-actions">
                <button
                  class="outline"
                  data-testid="empty-tile-open-agent"
                  onClick={() => {
                    layoutStore.setFocusedTile(tileId)
                    agentOps.handleOpenAgent()
                  }}
                >
                  <Icon icon={Bot} size="sm" />
                  {' '}
                  Open a new agent tab...
                </button>
                <button
                  class="outline"
                  data-testid="empty-tile-open-terminal"
                  onClick={() => {
                    layoutStore.setFocusedTile(tileId)
                    termOps.handleOpenTerminal()
                  }}
                >
                  <Icon icon={Terminal} size="sm" />
                  {' '}
                  Open a new terminal tab...
                </button>
              </div>
            </Show>
          </Show>
        </Show>
      </>
    )
  }

  const focusedAgentId = createMemo(() => {
    const tileId = layoutStore.focusedTileId()
    const tab = getActiveTabForTile(tileId)
    if (!tab || tab.type !== TabType.AGENT)
      return null
    return tab.id
  })

  // Refs for ChatDropZone integration: addFiles and triggerSend from AgentEditorPanel.
  const addFilesRef: { current: ((files: FileList | File[]) => Promise<number>) | undefined } = { current: undefined }
  const triggerSendRef: { current: (() => void) | undefined } = { current: undefined }

  // Clear refs when no agent is focused to avoid stale closures.
  createEffect(() => {
    if (!focusedAgentId()) {
      addFilesRef.current = undefined
      triggerSendRef.current = undefined
    }
  })

  const handleFileDrop = async (files: FileList, shiftKey: boolean) => {
    if (!addFilesRef.current)
      return
    const addedCount = await addFilesRef.current(files)
    if (shiftKey && addedCount > 0)
      triggerSendRef.current?.()
  }

  const FocusedAgentEditorPanel: Component<{ containerHeight: number }> = (props) => {
    const agentId = () => focusedAgentId()!
    return (
      <AgentEditorPanel
        agentId={agentId()}
        agent={agentStore.state.agents.find(a => a.id === agentId())}
        // eslint-disable-next-line solid/reactivity -- event handler, not a tracked scope
        onSendMessage={async (content, fileAttachments?: FileAttachment[]) => {
          const id = focusedAgentId()
          if (!id)
            return
          forceScrollToBottomRef.current?.()
          const sendAgent = agentStore.state.agents.find(a => a.id === id)

          // Build optimistic message JSON (attachments metadata only — no binary).
          const optimisticPayload: Record<string, unknown> = { content }
          if (fileAttachments && fileAttachments.length > 0) {
            optimisticPayload.attachments = fileAttachments.map(a => ({
              filename: a.filename,
              mime_type: a.mimeType,
            }))
          }

          // Create an optimistic local message so it appears immediately in the chat.
          const localId = `local-${crypto.randomUUID()}`
          const localMsg = {
            $typeName: 'leapmux.v1.AgentChatMessage' as const,
            id: localId,
            role: MessageRole.USER,
            content: new TextEncoder().encode(JSON.stringify(optimisticPayload)),
            contentCompression: ContentCompression.NONE,
            seq: 0n,
            createdAt: new Date().toISOString(),
            deliveryError: '',
            updatedAt: '',
            agentProvider: sendAgent?.agentProvider,
          }
          chatStore.addMessage(id, localMsg)

          try {
            const protoAttachments = fileAttachments?.map(a => ({
              filename: a.filename,
              mimeType: a.mimeType,
              data: a.data,
            })) ?? []

            await workerRpc.sendAgentMessage(sendAgent?.workerId ?? '', {
              agentId: id,
              content,
              attachments: protoAttachments,
            })
            // Keep the optimistic message until the persisted message arrives.
            // chatStore.addMessage() reconciles the matching server echo in place.
          }
          catch {
            chatStore.setMessageError(localId, 'Failed to deliver')
            chatStore.persistLocalMessage(id, localId, content, 'Failed to deliver')
          }
        }}
        addFilesRef={(fn) => { addFilesRef.current = fn }}
        triggerSendRef={(fn) => { triggerSendRef.current = fn }}
        disabled={false}
        focusRef={(fn) => { focusEditorRef.current = fn }}
        controlRequests={controlStore.getRequests(agentId())}
        onControlResponse={agentOps.handleControlResponse}
        onPermissionModeChange={mode => agentOps.handlePermissionModeChange(agentId(), mode)}
        onOptionGroupChange={(key, value) => agentOps.handleOptionGroupChange(agentId(), key, value)}
        onModelChange={v => agentOps.handleModelOrEffortChange(agentId(), 'model', v)}
        onEffortChange={v => agentOps.handleModelOrEffortChange(agentId(), 'effort', v)}
        onInterrupt={() => agentOps.handleInterrupt(agentId())}
        settingsLoading={settingsLoading.loading()}
        agentSessionInfo={agentSessionStore.getInfo(agentId())}
        agentWorking={agentThinking(agentId())}
        containerHeight={props.containerHeight}
      />
    )
  }

  const renderTile = (tileId: string) => (
    <Tile
      tileId={tileId}
      isFocused={layoutStore.focusedTileId() === tileId}
      canClose={hasMultipleTiles()}
      canSplit={layoutStore.canSplitTile(tileId)}
      tabBar={createTabBarForTile(tileId)}
      onFocus={() => {
        layoutStore.setFocusedTile(tileId)
        const tab = getActiveTabForTile(tileId)
        if (tab) {
          tabStore.setActiveTab(tab.type, tab.id)
          if (tab.type === TabType.AGENT) {
            agentStore.setActiveAgent(tab.id)
          }
          else if (tab.type === TabType.TERMINAL) {
            terminalStore.setActiveTerminal(tab.id)
          }
        }
      }}
      onSplitHorizontal={() => {
        layoutStore.splitTileHorizontal(tileId)
        persistLayout()
      }}
      onSplitVertical={() => {
        layoutStore.splitTileVertical(tileId)
        persistLayout()
      }}
      onClose={() => {
        layoutStore.closeTile(tileId)
        persistLayout()
      }}
      onPopOut={!opts.isFloatingWindowTile?.(tileId) && opts.onDetachTab && getActiveTabForTile(tileId)
        ? () => {
            const tab = getActiveTabForTile(tileId)
            if (tab)
              opts.onDetachTab!(tab)
          }
        : undefined}
    >
      {renderTileContent(tileId)}
    </Tile>
  )

  return {
    getActiveTabForTile,
    createTabBarForTile,
    tabBarElement,
    renderTileContent,
    focusedAgentId,
    FocusedAgentEditorPanel,
    renderTile,
    handleFileDrop,
    fileDropDisabled: () => {
      const agentId = focusedAgentId()
      if (!agentId)
        return true
      return controlStore.getRequests(agentId).length > 0
    },
  }
}
