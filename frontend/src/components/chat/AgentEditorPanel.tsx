import type { Component } from 'solid-js'
import type { EditorContentRef } from './ControlRequestBanner'
import type { AgentInfo } from '~/generated/leapmux/v1/agent_pb'
import type { AgentContextInfo } from '~/stores/agentContext.store'
import type { ControlRequest } from '~/stores/control.store'
import type { PermissionMode } from '~/utils/controlResponse'
import Check from 'lucide-solid/icons/check'
import ChevronDown from 'lucide-solid/icons/chevron-down'
import ChevronsDown from 'lucide-solid/icons/chevrons-down'
import ChevronsUp from 'lucide-solid/icons/chevrons-up'
import Copy from 'lucide-solid/icons/copy'
import Dot from 'lucide-solid/icons/dot'
import LoaderCircle from 'lucide-solid/icons/loader-circle'
import SendHorizontal from 'lucide-solid/icons/send-horizontal'
import Square from 'lucide-solid/icons/square'
import { createEffect, createSignal, createUniqueId, For, on, Show } from 'solid-js'
import { DropdownMenu } from '~/components/common/DropdownMenu'
import { createLoadingSignal } from '~/hooks/createLoadingSignal'
import { safeGetJson, safeGetString, safeRemoveItem, safeSetJson, safeSetString } from '~/lib/safeStorage'
import { interruptPulse, spinner } from '~/styles/animations.css'
import { iconSize } from '~/styles/tokens'
import { buildAllowResponse, buildDenyResponse, DEFAULT_EFFORT, DEFAULT_MODEL, EFFORT_LABELS, getToolName, MODEL_LABELS, PERMISSION_MODE_LABELS } from '~/utils/controlResponse'
import * as styles from './ChatView.css'
import { computePercentage, contextSize, ContextUsageGrid, DEFAULT_BUFFER_PCT } from './ContextUsageGrid'
import { ControlRequestActions, ControlRequestContent, trySubmitAskUserQuestion } from './ControlRequestBanner'
import { clearDraft, MarkdownEditor } from './MarkdownEditor'

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
  agentContextInfo?: AgentContextInfo
  agentWorking?: boolean
  /** Height of the parent container, used for max editor height calculation. */
  containerHeight?: number
}

const PERMISSION_MODES = Object.entries(PERMISSION_MODE_LABELS).map(([value, label]) => ({ value, label }))
const MODELS = Object.entries(MODEL_LABELS).map(([value, label]) => ({ value, label }))
const EFFORTS = Object.entries(EFFORT_LABELS).map(([value, label]) => ({ value, label }))

function modeLabel(mode: string): string {
  return PERMISSION_MODE_LABELS[mode as keyof typeof PERMISSION_MODE_LABELS] ?? 'Default'
}

function modelLabel(model: string): string {
  return MODELS.find(m => m.value === model)?.label ?? 'Sonnet'
}

// Module-level signal: shared across all editor instances
const EDITOR_MIN_HEIGHT = 38 // px – minimum height of the markdown editor wrapper
const EDITOR_MIN_HEIGHT_KEY = 'leapmux-editor-min-height'

function getStoredEditorMinHeight(): number | undefined {
  const stored = safeGetString(EDITOR_MIN_HEIGHT_KEY)
  if (stored) {
    const val = Number.parseInt(stored, 10)
    if (!Number.isNaN(val) && val >= EDITOR_MIN_HEIGHT)
      return val
  }
  return undefined
}

const [editorMinHeightSignal, setEditorMinHeight] = createSignal<number | undefined>(getStoredEditorMinHeight())

function formatTokenCount(tokens: number): string {
  if (tokens >= 1_000_000)
    return `${(tokens / 1_000_000).toFixed(1)}M`
  if (tokens >= 1_000)
    return `${(tokens / 1_000).toFixed(1)}k`
  return String(tokens)
}

export const AgentEditorPanel: Component<AgentEditorPanelProps> = (props) => {
  let panelRef: HTMLDivElement | undefined
  const menuId = createUniqueId()
  const [isDragging, setIsDragging] = createSignal(false)
  const [_editorContentHeight, setEditorContentHeight] = createSignal(0)
  const [hasContent, setHasContent] = createSignal(false)
  const { loading: sending, start: startSending } = createLoadingSignal()
  const interruptLoading = createLoadingSignal()

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

  // Track previous non-plan mode for Shift+Tab toggling.
  let previousNonPlanMode: PermissionMode = 'default'
  createEffect(() => {
    const mode = (props.agent?.permissionMode || 'default') as PermissionMode
    if (mode !== 'plan') {
      previousNonPlanMode = mode
    }
  })
  const togglePlanMode = () => {
    if (props.settingsLoading)
      return
    const currentMode = (props.agent?.permissionMode || 'default') as PermissionMode
    if (currentMode === 'plan') {
      props.onPermissionModeChange?.(previousNonPlanMode)
    }
    else {
      previousNonPlanMode = currentMode
      props.onPermissionModeChange?.('plan')
    }
  }

  // The first pending control request (if any).
  const activeControlRequest = () => {
    const reqs = props.controlRequests
    return reqs && reqs.length > 0 ? reqs[0] : null
  }

  const isAskUserQuestion = () => {
    const req = activeControlRequest()
    if (!req)
      return false
    const tool = getToolName(req.payload)
    return tool === 'AskUserQuestion' || tool === 'request_user_input'
  }

  // Whether the Interrupt button should be shown.
  const showInterrupt = () => !!props.agentWorking && !activeControlRequest()

  // Clear interrupt loading when the button hides.
  createEffect(on(showInterrupt, (show) => {
    if (!show) {
      interruptLoading.stop()
    }
  }))

  // Reset AskUserQuestion state when the active request changes.
  createEffect(on(
    () => activeControlRequest()?.requestId,
    (requestId) => {
      if (requestId && props.agentId) {
        const key = `leapmux-ask-state-${props.agentId}-${requestId}`
        const saved = safeGetJson<{ selections?: Record<number, string[]>, customTexts?: Record<number, string>, currentPage?: number }>(key)
        if (saved) {
          setAskSelections(saved.selections ?? {})
          setAskCustomTexts(saved.customTexts ?? {})
          setAskCurrentPage(saved.currentPage ?? 0)
          setHasContent(false)
          return
        }
      }
      setAskSelections({})
      setAskCustomTexts({})
      setAskCurrentPage(0)
      setHasContent(false)
    },
  ))

  // Persist AskUserQuestion selections to localStorage.
  createEffect(() => {
    const req = activeControlRequest()
    if (!req || !props.agentId || !isAskUserQuestion())
      return
    const key = `leapmux-ask-state-${props.agentId}-${req.requestId}`
    safeSetJson(key, {
      selections: askSelections(),
      customTexts: askCustomTexts(),
      currentPage: askCurrentPage(),
    })
  })

  let triggerSend: (() => void) | undefined

  const maxEditorHeight = () => {
    const h = props.containerHeight ?? 0
    return h > 0 ? Math.floor(h * 0.75) : 200
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
      if (val !== undefined && val > EDITOR_MIN_HEIGHT) {
        safeSetString(EDITOR_MIN_HEIGHT_KEY, String(val))
      }
      else {
        safeRemoveItem(EDITOR_MIN_HEIGHT_KEY)
      }
    }

    document.addEventListener('mousemove', onMouseMove)
    document.addEventListener('mouseup', onMouseUp)
  }

  const handleResizeReset = () => {
    setEditorMinHeight(undefined)
    safeRemoveItem(EDITOR_MIN_HEIGHT_KEY)
  }

  // Handles editor text for control requests.
  const sendControlResponse = (agentId: string, bytes: Uint8Array): Promise<void> => {
    return props.onControlResponse?.(agentId, bytes) ?? Promise.resolve()
  }

  const cleanupControlRequestDrafts = (requestId: string) => {
    if (!props.agentId)
      return
    clearDraft(`${props.agentId}-ctrl-${requestId}`)
    safeRemoveItem(`leapmux-ask-state-${props.agentId}-${requestId}`)
  }

  const handleControlSend = (content: string): boolean | void => {
    const req = activeControlRequest()
    if (!req)
      return
    if (isAskUserQuestion()) {
      const submitted = trySubmitAskUserQuestion(
        askState,
        req,
        content,
        sendControlResponse,
        editorContentRef,
      )
      if (!submitted)
        return false
      cleanupControlRequestDrafts(req.requestId)
      return
    }
    const toolName = getToolName(req.payload)
    const response = (content || toolName === 'ExitPlanMode')
      ? buildDenyResponse(req.requestId, content)
      : buildAllowResponse(req.requestId)
    const bytes = new TextEncoder().encode(JSON.stringify(response))
    sendControlResponse(req.agentId, bytes)
    cleanupControlRequestDrafts(req.requestId)
  }

  const handleSend = (content: string) => {
    props.onSendMessage(content)
  }

  const [sessionIdCopied, setSessionIdCopied] = createSignal(false)

  const handleCopySessionId = async () => {
    const sid = props.agent?.agentSessionId
    if (!sid)
      return
    try {
      await navigator.clipboard.writeText(sid)
      setSessionIdCopied(true)
      setTimeout(() => setSessionIdCopied(false), 2000)
    }
    catch {
      // ignore clipboard errors
    }
  }

  const hasContextInfo = () => {
    return props.agentContextInfo?.totalCostUsd != null || props.agentContextInfo?.contextUsage
  }

  const showInfoTrigger = () => !!props.agent?.agentSessionId || hasContextInfo()

  const infoHoverCardContent = () => (
    <>
      <Show when={props.agent?.agentSessionId}>
        <div class={styles.infoRow}>
          <span class={styles.infoLabel}>Session ID</span>
          <span class={styles.infoValue} data-testid="session-id-value">{props.agent?.agentSessionId}</span>
          <button
            class={styles.infoCopyButton}
            onClick={handleCopySessionId}
            title="Copy session ID"
            data-testid="session-id-copy"
          >
            <Show when={sessionIdCopied()} fallback={<Copy size={iconSize.xs} />}>
              <Check size={iconSize.xs} />
            </Show>
          </button>
        </div>
      </Show>
      <Show when={props.agent?.gitStatus?.branch}>
        <div class={styles.infoRow}>
          <span class={styles.infoLabel}>Branch</span>
          <span class={styles.infoValue}>
            {props.agent!.gitStatus!.branch}
            {(() => {
              const gs = props.agent!.gitStatus!
              const parts: string[] = []
              if (gs.ahead)
                parts.push(`+${gs.ahead}`)
              if (gs.behind)
                parts.push(`-${gs.behind}`)
              return parts.length > 0 ? ` [${parts.join(' ')}]` : ''
            })()}
          </span>
        </div>
        {(() => {
          const gs = props.agent!.gitStatus!
          const flags: string[] = []
          if (gs.conflicted)
            flags.push('Conflicted')
          if (gs.stashed)
            flags.push('Stashed')
          if (gs.modified)
            flags.push('Modified')
          if (gs.added)
            flags.push('Added')
          if (gs.deleted)
            flags.push('Deleted')
          if (gs.renamed)
            flags.push('Renamed')
          if (gs.typeChanged)
            flags.push('Type-changed')
          if (gs.untracked)
            flags.push('Untracked')
          return (
            <Show when={flags.length > 0}>
              <div class={styles.infoRow}>
                <span class={styles.infoLabel}>Status</span>
                <span class={styles.infoValue}>{flags.join(', ')}</span>
              </div>
            </Show>
          )
        })()}
      </Show>
      <Show when={props.agent?.workingDir}>
        <div class={styles.infoRow}>
          <span class={styles.infoLabel}>Directory</span>
          <span class={styles.infoValue}>{props.agent!.workingDir}</span>
        </div>
      </Show>
      <Show when={props.agentContextInfo?.contextUsage}>
        {(() => {
          const usage = props.agentContextInfo!.contextUsage!
          const ctxWindow = (usage.contextWindow && usage.contextWindow > 0) ? usage.contextWindow : 200_000
          const total = contextSize(usage)
          const pct = computePercentage(usage)
          return (
            <div class={styles.infoRow}>
              <span class={styles.infoLabel}>Context</span>
              <span class={styles.infoValue}>
                {formatTokenCount(total)}
                {` / ${formatTokenCount(ctxWindow)}`}
                {pct != null ? ` (${Math.round(pct)}% with ${DEFAULT_BUFFER_PCT}% buffer)` : ''}
              </span>
            </div>
          )
        })()}
      </Show>
      <Show when={props.agentContextInfo?.totalCostUsd != null}>
        <div class={styles.infoRow}>
          <span class={styles.infoLabel}>Cost</span>
          <span class={styles.infoValue}>
            $
            {props.agentContextInfo!.totalCostUsd!.toFixed(4)}
          </span>
        </div>
      </Show>
    </>
  )

  const currentModel = () => props.agent?.model || DEFAULT_MODEL
  const currentEffort = () => props.agent?.effort || DEFAULT_EFFORT
  const currentMode = () => props.agent?.permissionMode || 'default'

  const effortIcon = () => {
    switch (currentEffort()) {
      case 'low': return <ChevronsDown size={iconSize.xs} />
      case 'high': return <ChevronsUp size={iconSize.xs} />
      default: return <Dot size={iconSize.xs} />
    }
  }

  const settingsDropdown = () => (
    <DropdownMenu
      trigger={triggerProps => (
        <button
          class={styles.settingsTrigger}
          data-testid="agent-settings-trigger"
          disabled={props.disabled}
          {...triggerProps}
        >
          {modelLabel(currentModel())}
          {effortIcon()}
          {modeLabel(currentMode())}
          <Show when={props.settingsLoading} fallback={<ChevronDown size={iconSize.xs} />}>
            <LoaderCircle size={iconSize.xs} class={spinner} data-testid="settings-loading-spinner" />
          </Show>
        </button>
      )}
      class={styles.settingsMenu}
      data-testid="agent-settings-menu"
    >
      {/* Permission Mode */}
      <fieldset>
        <legend class={styles.settingsGroupLabel}>Permission Mode</legend>
        <For each={PERMISSION_MODES}>
          {mode => (
            <label
              role="menuitemradio"
              class={styles.settingsRadioItem}
              data-testid={`permission-mode-${mode.value}`}
            >
              <input
                type="radio"
                name={`${menuId}-mode`}
                value={mode.value}
                checked={currentMode() === mode.value}
                onChange={() => props.onPermissionModeChange?.(mode.value as PermissionMode)}
              />
              {mode.label}
            </label>
          )}
        </For>
      </fieldset>

      <hr class={styles.settingsSeparator} />

      {/* Model */}
      <fieldset>
        <legend class={styles.settingsGroupLabel}>Model</legend>
        <For each={MODELS}>
          {model => (
            <label
              role="menuitemradio"
              class={styles.settingsRadioItem}
              data-testid={`model-${model.value}`}
            >
              <input
                type="radio"
                name={`${menuId}-model`}
                value={model.value}
                checked={currentModel() === model.value}
                onChange={() => {
                  props.onModelChange?.(model.value)
                }}
              />
              {model.label}
            </label>
          )}
        </For>
      </fieldset>

      <hr class={styles.settingsSeparator} />

      {/* Effort */}
      <fieldset>
        <legend class={styles.settingsGroupLabel}>Effort</legend>
        <For each={EFFORTS}>
          {effort => (
            <label
              role="menuitemradio"
              class={styles.settingsRadioItem}
              data-testid={`effort-${effort.value}`}
            >
              <input
                type="radio"
                name={`${menuId}-effort`}
                value={effort.value}
                checked={currentEffort() === effort.value}
                onChange={() => {
                  props.onEffortChange?.(effort.value)
                }}
              />
              {effort.label}
            </label>
          )}
        </For>
      </fieldset>
    </DropdownMenu>
  )

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
          controlRequestId={activeControlRequest()?.requestId}
          onSend={activeControlRequest() ? handleControlSend : handleSend}
          disabled={props.disabled}
          onTogglePlanMode={togglePlanMode}
          requestedHeight={editorMinHeightSignal()}
          maxHeight={maxEditorHeight()}
          onContentHeightChange={setEditorContentHeight}
          onContentChange={(has) => {
            setHasContent(has)
            if (has && isAskUserQuestion()) {
              const page = askCurrentPage()
              setAskSelections(prev => (prev[page] ?? []).length > 0 ? { ...prev, [page]: [] } : prev)
            }
          }}
          sendRef={(fn) => { triggerSend = fn }}
          focusRef={(fn) => {
            props.focusRef?.(fn)
          }}
          contentRef={(get, set) => { editorContentRef = { get, set } }}
          placeholder={isAskUserQuestion() ? 'Type a custom answer...' : activeControlRequest() ? 'Type a rejection reason...' : undefined}
          allowEmptySend={!!activeControlRequest() && !isAskUserQuestion()}
          banner={
            activeControlRequest()
              ? (
                  <ControlRequestContent
                    request={activeControlRequest()!}
                    askState={askState}
                    optionsDisabled={hasContent()}
                  />
                )
              : undefined
          }
          footer={
            activeControlRequest()
              ? (
                  <ControlRequestActions
                    request={activeControlRequest()!}
                    askState={askState}
                    onRespond={(agentId, content) => {
                      const reqId = activeControlRequest()?.requestId
                      if (reqId)
                        cleanupControlRequestDrafts(reqId)
                      return props.onControlResponse?.(agentId, content) ?? Promise.resolve()
                    }}
                    hasEditorContent={hasContent()}
                    onTriggerSend={() => triggerSend?.()}
                    editorContentRef={editorContentRef}
                    infoTrigger={
                      showInfoTrigger()
                        ? (
                            <DropdownMenu
                              as="div"
                              trigger={triggerProps => (
                                <button
                                  class={styles.infoTrigger}
                                  data-testid="session-id-trigger"
                                  {...triggerProps}
                                >
                                  <ContextUsageGrid contextUsage={props.agentContextInfo?.contextUsage} size={iconSize.xs} />
                                </button>
                              )}
                              class="card"
                              data-testid="session-id-popover"
                            >
                              <div class={styles.infoRows}>
                                {infoHoverCardContent()}
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
                      {settingsDropdown()}
                      <Show when={showInfoTrigger()}>
                        <DropdownMenu
                          as="div"
                          trigger={triggerProps => (
                            <button
                              class={styles.infoTrigger}
                              data-testid="session-id-trigger"
                              {...triggerProps}
                            >
                              <ContextUsageGrid contextUsage={props.agentContextInfo?.contextUsage} size={iconSize.xs} />
                            </button>
                          )}
                          class="card"
                          data-testid="session-id-popover"
                        >
                          <div class={styles.infoRows}>
                            {infoHoverCardContent()}
                          </div>
                        </DropdownMenu>
                      </Show>
                    </div>
                    <div class={styles.footerBarRight}>
                      <Show when={showInterrupt()}>
                        <button
                          class={`${styles.interruptButton} ${interruptLoading.loading() ? '' : interruptPulse}`}
                          onClick={() => {
                            interruptLoading.start()
                            props.onInterrupt?.()
                          }}
                          disabled={interruptLoading.loading()}
                          data-testid="interrupt-button"
                        >
                          <Show when={interruptLoading.loading()} fallback={<Square size={iconSize.sm} />}>
                            <LoaderCircle size={iconSize.sm} class={spinner} />
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
                        <Show when={sending()} fallback={<SendHorizontal size={iconSize.sm} />}>
                          <LoaderCircle size={iconSize.sm} class={spinner} />
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
