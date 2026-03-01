import type { Component } from 'solid-js'
import type { EditorContentRef } from './controls/types'
import type { AgentInfo } from '~/generated/leapmux/v1/agent_pb'
import type { AgentSessionInfo } from '~/stores/agentSession.store'
import type { ControlRequest } from '~/stores/control.store'
import type { PermissionMode } from '~/utils/controlResponse'
import LoaderCircle from 'lucide-solid/icons/loader-circle'
import SendHorizontal from 'lucide-solid/icons/send-horizontal'
import Square from 'lucide-solid/icons/square'
import { createEffect, createSignal, on, onCleanup, onMount, Show } from 'solid-js'
import { DropdownMenu } from '~/components/common/DropdownMenu'
import { Icon } from '~/components/common/Icon'
import { createLoadingSignal } from '~/hooks/createLoadingSignal'
import { getResetsAt } from '~/lib/rateLimitUtils'
import { safeGetString, safeRemoveItem, safeSetString } from '~/lib/safeStorage'
import { registerEditorRef, unregisterEditorRef } from '~/stores/editorRef.store'
import { interruptPulse, spinner } from '~/styles/animations.css'
import { iconSize } from '~/styles/tokens'
import { useAgentInfoCard } from './AgentInfoCard'
import * as styles from './ChatView.css'
import { ContextUsageGrid } from './ContextUsageGrid'
import { ControlRequestActions, ControlRequestContent } from './ControlRequestBanner'
import { useControlResponseHandling } from './controlResponseHandling'
import { EditorSettingsDropdown } from './EditorSettingsDropdown'
import { MarkdownEditor } from './MarkdownEditor'

export interface AgentEditorPanelProps {
  agentId: string
  agent?: AgentInfo
  disabled?: boolean
  onSendMessage: (content: string) => void
  focusRef?: (focus: () => void) => void
  controlRequests?: ControlRequest[]
  onControlResponse?: (agentId: string, content: Uint8Array) => Promise<void>
  onPermissionModeChange?: (mode: PermissionMode) => void
  onModelChange?: (model: string) => void
  onEffortChange?: (effort: string) => void
  onInterrupt?: () => void
  settingsLoading?: boolean
  agentSessionInfo?: AgentSessionInfo
  agentWorking?: boolean
  /** Height of the parent container, used for max editor height calculation. */
  containerHeight?: number
}

// Per-agent editor height state
const EDITOR_MIN_HEIGHT = 38 // px - minimum height of the markdown editor wrapper
const EDITOR_MIN_HEIGHT_KEY_PREFIX = 'leapmux-editor-min-height-'

function editorMinHeightKey(agentId: string): string {
  return `${EDITOR_MIN_HEIGHT_KEY_PREFIX}${agentId}`
}

function getStoredEditorMinHeight(agentId: string): number | undefined {
  const stored = safeGetString(editorMinHeightKey(agentId))
  if (stored) {
    const val = Number.parseInt(stored, 10)
    if (!Number.isNaN(val) && val >= EDITOR_MIN_HEIGHT)
      return val
  }
  return undefined
}

// In-memory cache of per-agent heights (avoids localStorage reads on every render).
const editorMinHeightCache = new Map<string, number | undefined>()

export const AgentEditorPanel: Component<AgentEditorPanelProps> = (props) => {
  let panelRef: HTMLDivElement | undefined
  const [isDragging, setIsDragging] = createSignal(false)
  const [_editorContentHeight, setEditorContentHeight] = createSignal(0)
  const [hasContent, setHasContent] = createSignal(false)
  const { loading: sending, start: startSending } = createLoadingSignal()
  const interruptLoading = createLoadingSignal()

  // Per-agent editor min height: reactive signal that switches when agentId changes.
  const [editorMinHeightSignal, setEditorMinHeightSignal] = createSignal<number | undefined>(undefined)
  // Load per-agent height when agentId changes.
  createEffect(on(() => props.agentId, (agentId) => {
    if (!agentId)
      return
    if (!editorMinHeightCache.has(agentId)) {
      editorMinHeightCache.set(agentId, getStoredEditorMinHeight(agentId))
    }
    setEditorMinHeightSignal(editorMinHeightCache.get(agentId))
  }))
  const setEditorMinHeight = (val: number | undefined) => {
    setEditorMinHeightSignal(val)
    if (props.agentId)
      editorMinHeightCache.set(props.agentId, val)
  }

  // Shared state for AskUserQuestion selections
  const [askSelections, setAskSelections] = createSignal<Record<number, string[]>>({})
  const [askCustomTexts, setAskCustomTexts] = createSignal<Record<number, string>>({})
  const [askCurrentPage, setAskCurrentPage] = createSignal(0)
  const askState = {
    selections: askSelections,
    setSelections: setAskSelections,
    customTexts: askCustomTexts,
    setCustomTexts: setAskCustomTexts,
    currentPage: askCurrentPage,
    setCurrentPage: setAskCurrentPage,
  }

  // Editor content ref for programmatic get/set of editor markdown.
  let editorContentRef: EditorContentRef | undefined
  let editorFocusFn: (() => void) | undefined
  // Whether the MarkdownEditor has fully initialized (draft loaded, cursor restored).
  let editorReady = false

  // Track the agent ID for which the editor ref is registered.  props.agentId
  // is a reactive getter that may return null/undefined at cleanup time (e.g.
  // when the <Show> that controls this component unmounts because the focused
  // agent changed), so we must track the registered ID non-reactively.
  let registeredAgentId: string | null = null

  /** Register the editor ref if the editor is ready and both refs are available. */
  const tryRegisterEditorRef = (agentId: string) => {
    if (editorReady && editorContentRef && editorFocusFn) {
      registerEditorRef(agentId, { get: editorContentRef.get, set: editorContentRef.set, focus: editorFocusFn })
      registeredAgentId = agentId
    }
  }

  // Register/unregister editor refs with the global registry.
  onMount(() => {
    onCleanup(() => {
      if (registeredAgentId) {
        unregisterEditorRef(registeredAgentId)
        registeredAgentId = null
      }
    })
  })
  createEffect(on(() => props.agentId, (agentId, prevAgentId) => {
    if (prevAgentId) {
      unregisterEditorRef(prevAgentId)
      if (registeredAgentId === prevAgentId)
        registeredAgentId = null
    }
    tryRegisterEditorRef(agentId)
  }))

  const resetEditorHeight = () => {
    setEditorMinHeight(undefined)
    if (props.agentId)
      safeRemoveItem(editorMinHeightKey(props.agentId))
  }

  // Control response handling (extracted module)
  const ctrl = useControlResponseHandling(
    props,
    askState,
    () => editorContentRef,
    setHasContent,
    resetEditorHeight,
  )

  // Clear interrupt loading when the button hides.
  createEffect(on(ctrl.showInterrupt, (show) => {
    if (!show) {
      interruptLoading.stop()
    }
  }))

  // Agent info card (extracted module)
  const info = useAgentInfoCard(props)

  let triggerSend: (() => void) | undefined

  const maxEditorHeight = () => {
    const h = props.containerHeight ?? 0
    return h > 0 ? Math.floor(h * 0.5) : 200
  }

  const handleResizeStart = (e: MouseEvent) => {
    e.preventDefault()
    setIsDragging(true)
    const startY = e.clientY
    const maxHeight = maxEditorHeight()
    // Use the current visual height of the editor wrapper as the drag starting
    // point so the drag feels anchored to the handle's visual position.
    const editorWrapperEl = panelRef?.querySelector('[data-testid="chat-editor"]') as HTMLElement | null
    const startHeight = editorWrapperEl?.getBoundingClientRect().height
      ?? editorMinHeightSignal()
      ?? EDITOR_MIN_HEIGHT
    document.body.style.cursor = 'row-resize'

    const onMouseMove = (moveEvent: MouseEvent) => {
      const delta = startY - moveEvent.clientY
      const newMin = Math.max(EDITOR_MIN_HEIGHT, Math.min(maxHeight, startHeight + delta))
      setEditorMinHeight(newMin)
    }

    const onMouseUp = () => {
      setIsDragging(false)
      document.body.style.cursor = ''
      document.removeEventListener('mousemove', onMouseMove)
      document.removeEventListener('mouseup', onMouseUp)
      const val = editorMinHeightSignal()
      if (props.agentId) {
        if (val !== undefined && val > EDITOR_MIN_HEIGHT) {
          safeSetString(editorMinHeightKey(props.agentId), String(val))
        }
        else {
          safeRemoveItem(editorMinHeightKey(props.agentId))
        }
      }
    }

    document.addEventListener('mousemove', onMouseMove)
    document.addEventListener('mouseup', onMouseUp)
  }

  const handleResizeReset = () => {
    setEditorMinHeight(undefined)
    if (props.agentId)
      safeRemoveItem(editorMinHeightKey(props.agentId))
  }

  return (
    <div ref={panelRef} class={styles.editorPanelWrapper} data-testid="agent-editor-panel">
      <div
        class={`${styles.editorResizeHandle} ${isDragging() ? styles.editorResizeHandleActive : ''}`}
        data-testid="editor-resize-handle"
        on:mousedown={handleResizeStart}
        on:dblclick={handleResizeReset}
      />
      <div class={styles.inputArea}>
        <MarkdownEditor
          agentId={props.agentId}
          controlRequestId={ctrl.activeControlRequest()?.requestId}
          onSend={ctrl.activeControlRequest() ? ctrl.handleControlSend : ctrl.handleSend}
          disabled={props.disabled}
          onTogglePlanMode={ctrl.togglePlanMode}
          requestedHeight={editorMinHeightSignal()}
          maxHeight={maxEditorHeight()}
          onContentHeightChange={setEditorContentHeight}
          onContentChange={(has) => {
            setHasContent(has)
            // When the editor becomes empty and the manual height override
            // is at (or below) the minimum, clear it so the editor snaps
            // back to its natural single-line size.
            if (!has) {
              const h = editorMinHeightSignal()
              if (h !== undefined && h <= EDITOR_MIN_HEIGHT)
                resetEditorHeight()
            }
            if (has && ctrl.isAskUserQuestion()) {
              const page = askCurrentPage()
              setAskSelections(prev => (prev[page] ?? []).length > 0 ? { ...prev, [page]: [] } : prev)
            }
          }}
          sendRef={(fn) => { triggerSend = fn }}
          focusRef={(fn) => {
            editorFocusFn = fn
            props.focusRef?.(fn)
          }}
          contentRef={(get, set) => {
            editorContentRef = { get, set }
          }}
          onReady={() => {
            editorReady = true
            tryRegisterEditorRef(props.agentId)
          }}
          placeholder={ctrl.isAskUserQuestion() ? 'Type a custom answer...' : ctrl.activeControlRequest() ? 'Type a rejection reason...' : undefined}
          allowEmptySend={!!ctrl.activeControlRequest() && !ctrl.isAskUserQuestion()}
          banner={
            ctrl.activeControlRequest()
              ? (
                  <ControlRequestContent
                    request={ctrl.activeControlRequest()!}
                    askState={askState}
                    optionsDisabled={hasContent()}
                  />
                )
              : undefined
          }
          footer={
            ctrl.activeControlRequest()
              ? (
                  <ControlRequestActions
                    request={ctrl.activeControlRequest()!}
                    askState={askState}
                    onRespond={(agentId, content) => {
                      const reqId = ctrl.activeControlRequest()?.requestId
                      if (reqId)
                        ctrl.cleanupControlRequestDrafts(reqId)
                      resetEditorHeight()
                      return props.onControlResponse?.(agentId, content) ?? Promise.resolve()
                    }}
                    hasEditorContent={hasContent()}
                    onTriggerSend={() => triggerSend?.()}
                    editorContentRef={editorContentRef}
                    onPermissionModeChange={props.onPermissionModeChange}
                    infoTrigger={
                      info.showInfoTrigger()
                        ? (
                            <DropdownMenu
                              as="div"
                              trigger={triggerProps => (
                                <button
                                  class={styles.infoTrigger}
                                  data-testid="session-id-trigger"
                                  {...triggerProps}
                                >
                                  <ContextUsageGrid contextUsage={props.agentSessionInfo?.contextUsage} size={iconSize.xs} />
                                  <Show when={info.urgentRateLimit()}>
                                    {rl => (
                                      <span
                                        class={styles.rateLimitCountdown}
                                        title={(() => {
                                          const resetsAt = getResetsAt(rl().info)
                                          return resetsAt ? `Resets at ${new Date(resetsAt * 1000).toLocaleString()}` : undefined
                                        })()}
                                      >
                                        {rl().countdown}
                                      </span>
                                    )}
                                  </Show>
                                </button>
                              )}
                              class="card"
                              data-testid="session-id-popover"
                            >
                              <div class={styles.infoRows}>
                                {info.infoHoverCardContent()}
                              </div>
                            </DropdownMenu>
                          )
                        : undefined
                    }
                  />
                )
              : (
                  <div class={styles.footerBar}>
                    <div class={styles.footerBarLeft}>
                      <EditorSettingsDropdown
                        disabled={props.disabled}
                        settingsLoading={props.settingsLoading}
                        model={props.agent?.model}
                        effort={props.agent?.effort}
                        permissionMode={props.agent?.permissionMode}
                        onModelChange={props.onModelChange}
                        onEffortChange={props.onEffortChange}
                        onPermissionModeChange={props.onPermissionModeChange}
                      />
                      <Show when={info.showInfoTrigger()}>
                        <DropdownMenu
                          as="div"
                          trigger={triggerProps => (
                            <button
                              class={styles.infoTrigger}
                              data-testid="session-id-trigger"
                              {...triggerProps}
                            >
                              <ContextUsageGrid contextUsage={props.agentSessionInfo?.contextUsage} size={iconSize.xs} />
                              <Show when={info.urgentRateLimit()}>
                                {rl => (
                                  <span
                                    class={styles.rateLimitCountdown}
                                    title={(() => {
                                      const resetsAt = getResetsAt(rl().info)
                                      return resetsAt ? `Resets at ${new Date(resetsAt * 1000).toLocaleString()}` : undefined
                                    })()}
                                  >
                                    {rl().countdown}
                                  </span>
                                )}
                              </Show>
                            </button>
                          )}
                          class="card"
                          data-testid="session-id-popover"
                        >
                          <div class={styles.infoRows}>
                            {info.infoHoverCardContent()}
                          </div>
                        </DropdownMenu>
                      </Show>
                    </div>
                    <div class={styles.footerBarRight}>
                      <Show when={ctrl.showInterrupt()}>
                        <button
                          class={`${styles.interruptButton} ${interruptLoading.loading() ? '' : interruptPulse}`}
                          onClick={() => {
                            interruptLoading.start()
                            props.onInterrupt?.()
                          }}
                          disabled={interruptLoading.loading()}
                          data-testid="interrupt-button"
                        >
                          <Show when={interruptLoading.loading()} fallback={<Icon icon={Square} size="sm" />}>
                            <Icon icon={LoaderCircle} size="sm" class={spinner} />
                          </Show>
                          {interruptLoading.loading() ? 'Interrupting...' : 'Interrupt'}
                        </button>
                      </Show>
                      <button
                        type="button"
                        class={`${styles.sendButton} ${!hasContent() || props.disabled || sending() ? styles.sendButtonDisabled : ''}`}
                        disabled={!hasContent() || props.disabled || sending()}
                        onClick={() => {
                          startSending()
                          triggerSend?.()
                        }}
                        data-testid="send-button"
                      >
                        <Show when={sending()} fallback={<Icon icon={SendHorizontal} size="sm" />}>
                          <Icon icon={LoaderCircle} size="sm" class={spinner} />
                        </Show>
                        Send
                      </button>
                    </div>
                  </div>
                )
          }
        />
      </div>
    </div>
  )
}
