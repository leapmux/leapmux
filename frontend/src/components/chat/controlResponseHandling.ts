import type { Accessor } from 'solid-js'
import type { FileAttachment } from './attachments'
import type { AskQuestionState, EditorContentRef } from './controls/types'
import type { ProviderSettingChangeHandler } from './providerSettings'
import type { AgentProvider } from '~/generated/proto/leapmux/v1/agent_pb'
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
  agent?: { optionValues?: Record<string, string>, agentProvider?: AgentProvider }
  controlRequests?: ControlRequest[]
  onControlResponse?: (agentId: string, requestId: string, content: Uint8Array, claimToken?: string) => Promise<void>
  onSettingChange?: ProviderSettingChangeHandler
  onSendMessage: (content: string, attachments?: FileAttachment[]) => void
  settingsLoading?: boolean
  agentWorking?: boolean
  /**
   * Whether Interrupt can target THIS agent alone. False for a subagent tab
   * whose provider cannot interrupt one subagent (the worker would answer
   * FailedPrecondition), so the button is not offered at all. Defaults to true.
   */
  canInterrupt?: boolean
}

export interface ControlResponseHandlingResult {
  activeControlRequest: Accessor<ControlRequest | null>
  isAskUserQuestion: Accessor<boolean>
  showInterrupt: Accessor<boolean>
  handleControlSend: (content: string) => boolean | void
  handleSend: (content: string) => boolean | void
  cleanupControlRequestDrafts: (requestId: string) => void
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
    onChange({ sets: { [pm.groupKey]: decision.nextMode } })
  }

  // The first pending control request (if any).
  //
  // A MEMO, so that this notifies on the active request's IDENTITY and not on
  // every write to the list behind it. The store hands out a fresh array for
  // each queued sibling, each answer to a later request and each reconnect
  // sweep, and a plain thunk passes all of that churn to the composer: the
  // keyed owners in `AgentEditorPanel` re-run, rebuild the control components
  // and discard their local state, which unchecks the plan switches under a
  // user who checked them.
  const activeControlRequest = createMemo(() => {
    const reqs = props.controlRequests
    return reqs && reqs.length > 0 ? reqs[0] : null
  })

  const isAskUserQuestion = () => {
    const req = activeControlRequest()
    if (!req)
      return false
    const capability = pluginFor(props.agent?.agentProvider)?.askUserQuestion
    return capability?.isRequest(req.payload) ?? false
  }

  // Whether the Interrupt button should be shown.
  const showInterrupt = () =>
    !!props.agentWorking && !activeControlRequest() && (props.canInterrupt ?? true)

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
  // "Send feedback" button to disappear after a tab switch (A → B → A).
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

  // Answers as ONE request instance: the one the caller captured before it acted.
  // The worker's idempotency claim then keys on the instance the user answered,
  // and it still does after the store drops that instance -- the typed-feedback
  // and ask-question sibling of the footer action path in AgentEditorPanel,
  // which keys its owner on the request for the same reason. Reading the store
  // again inside the responder would answer as whatever is active by then, and
  // as no request at all once the queue is empty.
  const responderFor = (request: ControlRequest) => (agentId: string, bytes: Uint8Array): Promise<void> =>
    props.onControlResponse?.(agentId, request.requestId, bytes, request.claimToken) ?? Promise.resolve()

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
    const respond = responderFor(req)
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
      const capability = plugin.askUserQuestion
      if (!capability) {
        showWarnToast('Cannot send response: provider question support is incomplete')
        return false
      }
      const questions = capability.extractQuestions(req.payload)
      const sendAskResponse = () => {
        void capability.sendAnswer(req.agentId, respond, req.requestId, questions, askState, req.payload)
      }
      const submitted = trySubmitAskUserQuestion(
        askState,
        questions,
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
      const sent = respond(req.agentId, bytes)
      if (content.trim() && plugin.controlFeedbackAsFollowUpMessage?.(req.payload)) {
        void sent.then(() => props.onSendMessage(content)).catch(() => {})
      }
      else {
        void sent.catch(() => {})
      }
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
    showInterrupt,
    togglePlanMode,
  }
}
