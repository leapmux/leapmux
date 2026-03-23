import type { Accessor } from 'solid-js'
import type { AskQuestionState, EditorContentRef } from './controls/types'
import type { AgentProvider } from '~/generated/leapmux/v1/agent_pb'
import type { ControlRequest } from '~/stores/control.store'
import type { PermissionMode } from '~/utils/controlResponse'
import { createEffect, createMemo, on } from 'solid-js'
import { clearDraft } from '~/lib/editor/draftPersistence'
import { safeGetJson, safeRemoveItem, safeSetJson } from '~/lib/safeStorage'
import { buildAllowResponse, buildDenyResponse, getToolInput, getToolName } from '~/utils/controlResponse'
import { trySubmitAskUserQuestion } from './controls/AskUserQuestionControl'
import { getProviderPlugin } from './providers'

export interface ControlResponseHandlingProps {
  agentId: string
  agent?: { permissionMode?: string, codexCollaborationMode?: string, agentProvider?: AgentProvider }
  controlRequests?: ControlRequest[]
  onControlResponse?: (agentId: string, content: Uint8Array) => Promise<void>
  onPermissionModeChange?: (mode: PermissionMode) => void
  onOptionGroupChange?: (key: string, value: string) => void
  onSendMessage: (content: string) => void
  settingsLoading?: boolean
  agentWorking?: boolean
}

export interface ControlResponseHandlingResult {
  activeControlRequest: Accessor<ControlRequest | null>
  isAskUserQuestion: Accessor<boolean>
  showInterrupt: Accessor<boolean>
  handleControlSend: (content: string) => boolean | void
  handleSend: (content: string) => boolean | void
  cleanupControlRequestDrafts: (requestId: string) => void
  sendControlResponse: (agentId: string, bytes: Uint8Array) => Promise<void>
  togglePlanMode: () => void
  resetEditorHeight: () => void
}

export function useControlResponseHandling(
  props: ControlResponseHandlingProps,
  askState: AskQuestionState,
  editorContentRefAccessor: () => EditorContentRef | undefined,
  resetEditorHeightFn: () => void,
): ControlResponseHandlingResult {
  const planModeConfig = () => getProviderPlugin(props.agent?.agentProvider)?.planMode

  // Track previous non-plan mode for Shift+Tab toggling.
  let previousNonPlanMode = planModeConfig()?.defaultValue ?? 'default'
  createEffect(() => {
    const pm = planModeConfig()
    if (!pm)
      return
    const mode = pm.currentMode(props.agent || {})
    if (mode !== pm.planValue) {
      previousNonPlanMode = mode
    }
  })
  const togglePlanMode = () => {
    if (props.settingsLoading)
      return
    const pm = planModeConfig()
    if (!pm)
      return
    const callbacks = { onPermissionModeChange: props.onPermissionModeChange, onOptionGroupChange: props.onOptionGroupChange }
    const currentMode = pm.currentMode(props.agent || {})
    if (currentMode === pm.planValue) {
      pm.setMode(previousNonPlanMode, callbacks)
    }
    else {
      previousNonPlanMode = currentMode
      pm.setMode(pm.planValue, callbacks)
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
    const plugin = props.agent?.agentProvider != null
      ? getProviderPlugin(props.agent.agentProvider)
      : undefined
    return plugin?.isAskUserQuestion?.(req.payload) ?? false
  }

  // Whether the Interrupt button should be shown.
  const showInterrupt = () => !!props.agentWorking && !activeControlRequest()

  // Memoize the active request ID so that the effect below only fires when
  // the value actually changes. Without this, reactive store updates
  // (e.g. controlStore.clearAgent during WebSocket reconnect) re-trigger the
  // deps function even when the result is the same `undefined`, causing
  // hasContent to be reset and disabling the send button after page refresh.
  const activeRequestId = createMemo(() => activeControlRequest()?.requestId)

  // Reset AskUserQuestion state when the active request changes.
  // NOTE: Do NOT call setHasContent(false) here.  The MarkdownEditor's
  // controlRequestId swap effect is the authoritative source for editor
  // content state — it loads the correct draft and calls onContentChange.
  // Resetting hasContent here races with the MarkdownEditor and causes the
  // "Send Feedback" button to disappear after a tab switch (A → B → A).
  createEffect(on(
    activeRequestId,
    (requestId) => {
      if (requestId && props.agentId) {
        const key = `leapmux-ask-state-${props.agentId}-${requestId}`
        const saved = safeGetJson<{ selections?: Record<number, string[]>, customTexts?: Record<number, string>, currentPage?: number }>(key)
        if (saved) {
          askState.setSelections(saved.selections ?? {})
          askState.setCustomTexts(saved.customTexts ?? {})
          askState.setCurrentPage(saved.currentPage ?? 0)
          return
        }
      }
      askState.setSelections({})
      askState.setCustomTexts({})
      askState.setCurrentPage(0)
    },
  ))

  // Persist AskUserQuestion selections to localStorage.
  createEffect(() => {
    const req = activeControlRequest()
    if (!req || !props.agentId || !isAskUserQuestion())
      return
    const key = `leapmux-ask-state-${props.agentId}-${req.requestId}`
    safeSetJson(key, {
      selections: askState.selections(),
      customTexts: askState.customTexts(),
      currentPage: askState.currentPage(),
    })
  })

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
        editorContentRefAccessor(),
      )
      if (!submitted)
        return false
      cleanupControlRequestDrafts(req.requestId)
      resetEditorHeightFn()
      return
    }
    const toolName = getToolName(req.payload)
    const response = (content || toolName === 'ExitPlanMode')
      ? buildDenyResponse(req.requestId, content)
      : buildAllowResponse(req.requestId, getToolInput(req.payload))
    if (toolName === 'CodexPlanModePrompt')
      (response as Record<string, unknown>).codexPlanModePrompt = true
    const bytes = new TextEncoder().encode(JSON.stringify(response))
    sendControlResponse(req.agentId, bytes)
    cleanupControlRequestDrafts(req.requestId)
    resetEditorHeightFn()
  }

  const handleSend = (content: string): boolean | void => {
    if (content.trim().length < 1)
      return false
    props.onSendMessage(content)
    resetEditorHeightFn()
  }

  return {
    activeControlRequest,
    cleanupControlRequestDrafts,
    handleControlSend,
    handleSend,
    isAskUserQuestion,
    resetEditorHeight: resetEditorHeightFn,
    sendControlResponse,
    showInterrupt,
    togglePlanMode,
  }
}
