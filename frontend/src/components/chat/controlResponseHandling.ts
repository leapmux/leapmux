import type { Accessor, Setter } from 'solid-js'
import type { AskQuestionState, EditorContentRef } from './controls/types'
import type { ControlRequest } from '~/stores/control.store'
import type { PermissionMode } from '~/utils/controlResponse'
import { createEffect, createMemo, on } from 'solid-js'
import { clearDraft } from '~/lib/editor/draftPersistence'
import { safeGetJson, safeRemoveItem, safeSetJson } from '~/lib/safeStorage'
import { buildAllowResponse, buildDenyResponse, getToolName } from '~/utils/controlResponse'
import { trySubmitAskUserQuestion } from './controls/AskUserQuestionControl'

export interface ControlResponseHandlingProps {
  agentId: string
  agent?: { permissionMode?: string }
  controlRequests?: ControlRequest[]
  onControlResponse?: (agentId: string, content: Uint8Array) => Promise<void>
  onPermissionModeChange?: (mode: PermissionMode) => void
  onSendMessage: (content: string) => void
  settingsLoading?: boolean
  agentWorking?: boolean
}

export interface ControlResponseHandlingResult {
  activeControlRequest: Accessor<ControlRequest | null>
  isAskUserQuestion: Accessor<boolean>
  showInterrupt: Accessor<boolean>
  handleControlSend: (content: string) => boolean | void
  handleSend: (content: string) => void
  cleanupControlRequestDrafts: (requestId: string) => void
  sendControlResponse: (agentId: string, bytes: Uint8Array) => Promise<void>
  togglePlanMode: () => void
  resetEditorHeight: () => void
}

export function useControlResponseHandling(
  props: ControlResponseHandlingProps,
  askState: AskQuestionState,
  editorContentRefAccessor: () => EditorContentRef | undefined,
  setHasContent: Setter<boolean>,
  resetEditorHeightFn: () => void,
): ControlResponseHandlingResult {
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

  // Memoize the active request ID so that the effect below only fires when
  // the value actually changes. Without this, reactive store updates
  // (e.g. controlStore.clearAgent during WebSocket reconnect) re-trigger the
  // deps function even when the result is the same `undefined`, causing
  // hasContent to be reset and disabling the send button after page refresh.
  const activeRequestId = createMemo(() => activeControlRequest()?.requestId)

  // Reset AskUserQuestion state when the active request changes.
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
          setHasContent(false)
          return
        }
      }
      askState.setSelections({})
      askState.setCustomTexts({})
      askState.setCurrentPage(0)
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
      : buildAllowResponse(req.requestId)
    const bytes = new TextEncoder().encode(JSON.stringify(response))
    sendControlResponse(req.agentId, bytes)
    cleanupControlRequestDrafts(req.requestId)
    resetEditorHeightFn()
  }

  const handleSend = (content: string) => {
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
