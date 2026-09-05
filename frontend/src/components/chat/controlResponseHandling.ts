import type { Accessor } from 'solid-js'
import type { FileAttachment } from './attachments'
import type { ControlAnswerSeed, ControlAnswerState, EditorContentRef } from './controls/types'
import type { ProviderSettingChangeHandler } from './providerSettings'
import type { AgentProvider } from '~/generated/proto/leapmux/v1/agent_pb'
import type { ControlRequest } from '~/stores/control.store'
import { createEffect, createMemo, on } from 'solid-js'
import { showWarnToast } from '~/components/common/Toast'
import { localStorageGet, localStorageRemove, localStorageSet, PREFIX_CONTROL_STATE } from '~/lib/browserStorage'
import { clearDraft } from '~/lib/editor/draftPersistence'
import { requestInstanceId } from '~/stores/control.store'
import { controlQuestion, trySubmitAskUserQuestion } from './controls/AskUserQuestionControl'
import { decidePlanModeToggle } from './planModeToggle'
import { pluginFor } from './providers/registry'
import './providers'

export interface ControlResponseHandlingProps {
  agentId: string
  agent?: { optionValues?: Record<string, string>, agentProvider?: AgentProvider }
  controlRequests?: ControlRequest[]
  onControlResponse?: (request: ControlRequest, content: Uint8Array) => Promise<void>
  onSettingChange?: ProviderSettingChangeHandler
  onSendMessage: (content: string, attachments?: FileAttachment[]) => void | Promise<void>
  onSendControlFeedback?: (content: string) => void | Promise<void>
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
  handleSend: (content: string) => boolean | void | Promise<boolean | void>
  /** Discards ONE answered request's drafts and collapses the composer. */
  finishAnswer: (request: ControlRequest) => void
  /** Builds the responder that answers as ONE request instance. See `respondTo` below. */
  respondTo: (request: ControlRequest) => (bytes: Uint8Array) => Promise<void>
  togglePlanMode: () => void
}

export function useControlResponseHandling(
  props: ControlResponseHandlingProps,
  answerState: ControlAnswerState,
  editorContentRefAccessor: () => EditorContentRef | undefined,
  resetEditorHeightFn: () => void,
  getAttachments?: () => FileAttachment[],
  onSendMessageOverride?: (content: string, attachments?: FileAttachment[]) => void | Promise<void>,
): ControlResponseHandlingResult {
  let sendInFlight = false
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
  // A MEMO, so that this notifies on the active request's IDENTITY. A plain
  // thunk notifies on every write to the list behind it instead. The list is a
  // Solid store array, so each write to it notifies every reader of it. A
  // queued sibling, an answer to a later request, and a reconnect sweep each
  // write that list.
  //
  // A notification that reaches the composer re-runs the keyed owners in
  // `AgentEditorPanel`. They rebuild the control components and discard their
  // local state, which unchecks the plan switches that the user checked. The
  // memo returns the same instance when the head does not change, so it stops
  // the notification there.
  const activeControlRequest = createMemo(() => props.controlRequests?.[0] ?? null)

  // The active request's questions, or undefined when it is not a question.
  // `controlQuestion` is the one classifier; see its doc comment.
  const activeQuestion = createMemo(() => controlQuestion(activeControlRequest(), props.agent?.agentProvider))
  const isAskUserQuestion = createMemo(() => activeQuestion() !== undefined)

  // Whether the Interrupt button should be shown.
  const showInterrupt = () =>
    !!props.agentWorking && !activeControlRequest() && (props.canInterrupt ?? true)

  // The saved answer of ONE request instance -- its selections, its typed notes,
  // its page and its switches. `requestInstanceId` states why a request id alone
  // is not enough.
  const answerKey = (request: ControlRequest) =>
    `${PREFIX_CONTROL_STATE}${props.agentId}:${requestInstanceId(request)}`

  // The request whose answer `answerState` holds right now. The restore effect
  // below assigns it; the persist effect reads it. See both for why.
  let answerOwner: ControlRequest | null = null

  // Reset the user's in-progress answer when the active request INSTANCE changes.
  //
  // The dependency is the request, not its ID. The agent reuses a request_id,
  // and the store admits a second instance of that ID with a different payload
  // (see `addRequest` in `control.store.ts`). An ID dependency does not notify
  // for that swap, so the new instance inherits the answers of the instance
  // that the user already answered, and one Submit sends them.
  //
  // `activeControlRequest` is a memo, so it still absorbs the repeated store
  // writes that an ID memo absorbed before. `controlStore.clearAgent` during a
  // WebSocket reconnect installs a fresh empty list, and the memo reports the
  // same `null` for it. Without that gate the effect resets `hasContent` and
  // disables the send button after a page refresh.
  //
  // NOTE: Do NOT call setHasContent(false) here.  The MarkdownEditor's
  // controlRequestId swap effect is the authoritative source for editor
  // content state — it loads the correct draft and calls onContentChange.
  // Resetting hasContent here races with the MarkdownEditor and causes the
  // "Send feedback" button to disappear after a tab switch (A → B → A).
  //
  // The effect also records WHOSE answer `answerState` now holds, for the persist
  // effect below.
  createEffect(on(
    activeControlRequest,
    (request) => {
      answerOwner = request
      if (request && props.agentId) {
        const saved = localStorageGet<ControlAnswerSeed>(answerKey(request))
        if (saved) {
          answerState.setSelections(saved.selections ?? {})
          answerState.setCustomTexts(saved.customTexts ?? {})
          answerState.setCurrentPage(saved.currentPage ?? 0)
          answerState.setSwitches(saved.switches ?? {})
          return
        }
      }
      answerState.setSelections({})
      answerState.setCustomTexts({})
      answerState.setCurrentPage(0)
      answerState.setSwitches({})
    },
  ))

  // Persist the answers to localStorage, under the key of the request they
  // BELONG to.
  //
  // The owner is a plain variable, not the active request. A swap makes this
  // effect and the restore effect above both stale, and Solid gives no order
  // between them that this code may rely on. Reading the active request here
  // would let this effect run first and write the outgoing request's answers
  // under the incoming request's key. The restore effect then reads that key
  // back, and the new prompt opens already answered -- one Submit click sends
  // an answer the user never gave for it.
  //
  // Whichever order the two effects take, the owner and the answers move
  // together: the restore effect writes both, so this effect re-runs after it
  // and stores the new owner's own answers.
  createEffect(() => {
    const value: ControlAnswerSeed = {
      selections: answerState.selections(),
      customTexts: answerState.customTexts(),
      currentPage: answerState.currentPage(),
      switches: answerState.switches(),
    }
    const owner = answerOwner
    // EVERY control request, not only a question. A permission prompt and a plan
    // approval carry switches, and those are exactly what a rebuild discards.
    if (!owner || !props.agentId)
      return
    localStorageSet(answerKey(owner), value)
  })

  // Answers as ONE request instance: the one the caller captured before it acted.
  // The worker's idempotency claim then keys on the instance the user answered.
  // It still keys on that instance after the store drops it. Both answer paths
  // use this: the typed feedback below, and the footer action row in
  // `AgentEditorPanel`, which keys its owner on the request for the same reason.
  //
  // Reading the store again inside the responder would answer as whatever is
  // active by then. Once the queue is empty it would answer as no request at all.
  const respondTo = (request: ControlRequest) => (bytes: Uint8Array): Promise<void> =>
    props.onControlResponse?.(request, bytes) ?? Promise.resolve()

  // Discards every draft of ONE answered request: its editor text, its per-page
  // question answers, and its saved selection state.
  const cleanupControlRequestDrafts = (request: ControlRequest) => {
    if (!props.agentId)
      return
    const instanceId = requestInstanceId(request)
    clearDraft(`${props.agentId}-ctrl-${instanceId}`)
    // The editor scopes a question's draft per page, and it writes one key per
    // question. The same classifier that decides those pages counts them here,
    // so a question set of any size loses every key it wrote. A request that is
    // not a question wrote none, and the loop then does no work.
    const pages = controlQuestion(request, props.agent?.agentProvider)?.questions.length ?? 0
    for (let page = 0; page < pages; page++) {
      clearDraft(`${props.agentId}-ctrl-${instanceId}-q-${page}`)
    }
    localStorageRemove(answerKey(request))
    // Release the ownership too. The persist effect is deferred, so it runs
    // AFTER this cleanup: `trySubmitAskUserQuestion` writes `answerState` on its
    // way in, and the effect would then re-write the key that the line above
    // just deleted. The answered instance's answers would outlive it. The
    // restore effect assigns the next owner when the head changes.
    if (answerOwner === request)
      answerOwner = null
  }

  // The epilogue of ONE answered request: discard its drafts, then collapse the
  // composer back to its natural height. Every answer path ends here -- the
  // footer action row in `AgentEditorPanel` and both branches of
  // `handleControlSend` -- so the two steps cannot drift apart.
  //
  // This deliberately sits OUTSIDE `respondTo`. The non-question branch below
  // runs the epilogue even when the plugin builds no response, and a plugin
  // decides for itself how many times it calls the responder.
  const finishAnswer = (request: ControlRequest) => {
    cleanupControlRequestDrafts(request)
    resetEditorHeightFn()
  }

  const handleControlSend = (content: string): boolean | void => {
    const req = activeControlRequest()
    if (!req)
      return
    const respond = respondTo(req)
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
    // Classify the CAPTURED request, not whatever the store holds by now. The
    // shared return type carries the capability, so a question with no capability
    // to answer it cannot be represented here.
    const question = controlQuestion(req, provider)
    if (question) {
      const sendAskResponse = () => {
        void question.capability.sendAnswer(req, respond, question.questions, answerState)
      }
      const submitted = trySubmitAskUserQuestion(
        answerState,
        question.questions,
        content,
        sendAskResponse,
        editorContentRefAccessor(),
        Boolean(plugin.preservesSelectionNotes),
      )
      if (!submitted)
        return false
      finishAnswer(req)
      return
    }
    const response = plugin.buildControlResponse?.(req.payload, content, req.requestId)
    if (response) {
      const bytes = new TextEncoder().encode(JSON.stringify(response))
      const sent = respond(bytes)
      if (content.trim() && plugin.controlFeedbackAsFollowUpMessage?.(req.payload)) {
        void sent.then(() => (props.onSendControlFeedback ?? props.onSendMessage)(content)).catch(() => {})
      }
      else {
        void sent.catch(() => {})
      }
    }
    finishAnswer(req)
  }

  const handleSend = (content: string): boolean | void | Promise<boolean | void> => {
    const currentAttachments = getAttachments?.() ?? []
    if (content.trim().length < 1 && currentAttachments.length === 0)
      return false
    if (sendInFlight)
      return false
    const sendFn = onSendMessageOverride ?? props.onSendMessage
    sendInFlight = true
    let sent: void | Promise<void>
    try {
      sent = sendFn(content, currentAttachments.length > 0 ? currentAttachments : undefined)
    }
    catch (error) {
      sendInFlight = false
      throw error
    }
    if (sent && typeof sent.then === 'function') {
      return Promise.resolve(sent).then(() => {
        resetEditorHeightFn()
      }).finally(() => {
        sendInFlight = false
      })
    }
    sendInFlight = false
    resetEditorHeightFn()
  }

  return {
    activeControlRequest,
    finishAnswer,
    handleControlSend,
    handleSend,
    isAskUserQuestion,
    respondTo,
    showInterrupt,
    togglePlanMode,
  }
}
