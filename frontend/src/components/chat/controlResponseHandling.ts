import type { Accessor } from 'solid-js'
import type { FileAttachment } from './attachments'
import type { AskQuestionState, EditorContentRef } from './controls/types'
import type { ProviderSettingChange } from './providers/registry'
import type { AgentProvider } from '~/generated/leapmux/v1/agent_pb'
import type { ControlRequest } from '~/stores/control.store'
import { createEffect, createMemo, on } from 'solid-js'
import { showWarnToast } from '~/components/common/Toast'
import { localStorageGet, localStorageRemove, localStorageSet, PREFIX_ASK_STATE } from '~/lib/browserStorage'
import { clearDraft } from '~/lib/editor/draftPersistence'
import { trySubmitAskUserQuestion } from './controls/AskUserQuestionControl'
import { decidePlanModeToggle } from './planModeToggle'
import { pluginFor } from './providers/registry'
import './providers'

export interface ControlResponseHandlingProps {
  agentId: string
  agent?: { permissionMode?: string, extraSettings?: Record<string, string>, agentProvider?: AgentProvider }
  controlRequests?: ControlRequest[]
  onControlResponse?: (agentId: string, content: Uint8Array) => Promise<void>
  onSettingChange?: (change: ProviderSettingChange) => void
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
  const planModeConfig = () => pluginFor(props.agent?.agentProvider)?.planMode

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
    const onChange = props.onSettingChange
    if (!pm || !onChange)
      return
    const currentMode = pm.currentMode(props.agent || {})
    const decision = decidePlanModeToggle({ currentMode, planValue: pm.planValue, previousNonPlanMode })
    if (decision.updatePreviousNonPlanMode !== undefined)
      previousNonPlanMode = decision.updatePreviousNonPlanMode
    pm.setMode(decision.nextMode, onChange)
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
    const plugin = pluginFor(props.agent?.agentProvider)
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
        const saved = localStorageGet<{ selections?: Record<number, string[]>, customTexts?: Record<number, string>, currentPage?: number }>(key)
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
    localStorageSet(key, {
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
    localStorageRemove(`${PREFIX_ASK_STATE}${props.agentId}:${requestId}`)
  }

  const handleControlSend = (content: string): boolean | void => {
    const req = activeControlRequest()
    if (!req)
      return
    // Resolve the agent's own provider plugin -- no Claude fallback. A live agent
    // always carries a real provider, so a missing plugin means an UNSPECIFIED or
    // unregistered provider (a bug, e.g. backend/frontend version skew). Refuse to
    // encode a control response through the wrong provider's builder; surface a
    // toast so the send is not a silent no-op, and keep the editor content.
    const provider = props.agent?.agentProvider
    const plugin = pluginFor(provider)
    if (!plugin) {
      showWarnToast(`Cannot send response: unsupported agent provider (${provider})`)
      return false
    }
    if (isAskUserQuestion()) {
      const normalizedQuestions = plugin.extractAskUserQuestions?.(req.payload) ?? []
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
        void plugin.sendAskUserQuestionResponse?.(req.agentId, sendControlResponse, req.requestId, normalizedQuestions, askState, req.payload)
      }
      const submitted = trySubmitAskUserQuestion(
        askState,
        normalizedRequest,
        content,
        sendAskResponse,
        editorContentRefAccessor(),
        Boolean(plugin.preservesSelectionNotes),
      )
      if (!submitted)
        return false
      cleanupControlRequestDrafts(req.requestId)
      resetEditorHeightFn()
      return
    }
    const response = plugin.buildControlResponse?.(req.payload, content, req.requestId)
    if (response) {
      const bytes = new TextEncoder().encode(JSON.stringify(response))
      void sendControlResponse(req.agentId, bytes)
    }
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
