import type { Accessor } from 'solid-js'
import type { FileAttachment } from './attachments'
import type { AskQuestionState, EditorContentRef, Question } from './controls/types'
import type { ControlRequest } from '~/stores/control.store'
import type { PermissionMode } from '~/utils/controlResponse'
import { createEffect, createMemo, on } from 'solid-js'
import { AgentProvider } from '~/generated/leapmux/v1/agent_pb'
import { PREFIX_ASK_STATE, safeGetJson, safeRemoveItem, safeSetJson } from '~/lib/browserStorage'
import { clearDraft } from '~/lib/editor/draftPersistence'
import { buildAllowResponse, buildDenyResponse, getToolInput, getToolName } from '~/utils/controlResponse'
import { buildAskAnswers, trySubmitAskUserQuestion } from './controls/AskUserQuestionControl'
import { sendCodexUserInputResponse } from './controls/CodexControlRequest'
import { getCursorQuestions, sendCursorQuestionResponse } from './controls/CursorControlRequest'
import { sendOpenCodeQuestionResponse } from './controls/OpenCodeControlRequest'
import { decidePlanModeToggle } from './planModeToggle'
import { getProviderPlugin } from './providers/registry'
import './providers'

export interface ControlResponseHandlingProps {
  agentId: string
  agent?: { permissionMode?: string, extraSettings?: Record<string, string>, agentProvider?: AgentProvider }
  controlRequests?: ControlRequest[]
  onControlResponse?: (agentId: string, content: Uint8Array) => Promise<void>
  onPermissionModeChange?: (mode: PermissionMode) => void
  onOptionGroupChange?: (key: string, value: string) => void
  onSendMessage: (content: string, attachments?: FileAttachment[]) => void
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
  getAttachments?: () => FileAttachment[],
  onSendMessageOverride?: (content: string, attachments?: FileAttachment[]) => void,
): ControlResponseHandlingResult {
  const planModeConfig = () => props.agent?.agentProvider ? getProviderPlugin(props.agent.agentProvider)?.planMode : undefined

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
    const decision = decidePlanModeToggle({ currentMode, planValue: pm.planValue, previousNonPlanMode })
    if (decision.updatePreviousNonPlanMode !== undefined)
      previousNonPlanMode = decision.updatePreviousNonPlanMode
    pm.setMode(decision.nextMode, callbacks)
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
        const key = `${PREFIX_ASK_STATE}${props.agentId}:${requestId}`
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
    const key = `${PREFIX_ASK_STATE}${props.agentId}:${req.requestId}`
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
    // Ask-user-question drafts may be scoped per page; clear a reasonable
    // range of page keys for this one-shot request.
    for (let page = 0; page < 20; page++) {
      clearDraft(`${props.agentId}-ctrl-${requestId}-q-${page}`)
    }
    safeRemoveItem(`${PREFIX_ASK_STATE}${props.agentId}:${requestId}`)
  }

  const handleControlSend = (content: string): boolean | void => {
    const req = activeControlRequest()
    if (!req)
      return
    if (isAskUserQuestion()) {
      const provider = props.agent?.agentProvider
      // Extract and normalize questions once for both page navigation and response building.
      const normalizedQuestions: Question[] = (() => {
        switch (provider) {
          case AgentProvider.CODEX: {
            const params = req.payload.params as Record<string, unknown> | undefined
            return (params?.questions as Question[] | undefined) ?? []
          }
          case AgentProvider.OPENCODE: {
            const properties = req.payload.properties as Record<string, unknown> | undefined
            const rawQuestions = (properties?.questions as Array<Record<string, unknown>> | undefined) ?? []
            return rawQuestions.map(question => ({
              ...question,
              multiSelect: (question.multiSelect as boolean | undefined) ?? (question.multiple as boolean | undefined),
            })) as Question[]
          }
          case AgentProvider.CURSOR:
            return getCursorQuestions(req.payload)
          default:
            return (getToolInput(req.payload).questions as Question[] | undefined) ?? []
        }
      })()
      const normalizedRequest: ControlRequest = {
        ...req,
        payload: {
          ...req.payload,
          request: {
            tool_name: 'AskUserQuestion',
            input: { questions: normalizedQuestions },
          },
        },
      }
      const sendAskResponse = () => {
        switch (provider) {
          case AgentProvider.CODEX:
            void sendCodexUserInputResponse(req.agentId, sendControlResponse, req.requestId, normalizedQuestions, askState)
            break
          case AgentProvider.OPENCODE:
            void sendOpenCodeQuestionResponse(req.agentId, sendControlResponse, req.requestId, normalizedQuestions, askState)
            break
          case AgentProvider.CURSOR:
            void sendCursorQuestionResponse(req.agentId, sendControlResponse, req.requestId, normalizedQuestions, askState)
            break
          default: {
            const response = buildAskAnswers(askState, normalizedQuestions, getToolInput(req.payload), req.requestId)
            void sendControlResponse(req.agentId, new TextEncoder().encode(JSON.stringify(response)))
          }
        }
      }
      const submitted = trySubmitAskUserQuestion(
        askState,
        normalizedRequest,
        content,
        sendAskResponse,
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
    const currentAttachments = getAttachments?.() ?? []
    if (content.trim().length < 1 && currentAttachments.length === 0)
      return false
    const sendFn = onSendMessageOverride ?? props.onSendMessage
    sendFn(content, currentAttachments.length > 0 ? currentAttachments : undefined)
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
