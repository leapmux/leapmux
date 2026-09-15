import type { Accessor } from 'solid-js'
import type { FileAttachment } from './attachments'
import type { ControlSurface } from './controls/controlSurface'
import type { ControlAnswerSeed, ControlAnswerState, ControlResponseHandler, ControlResponseOptions, ControlResponseSender, EditorContentRef } from './controls/types'
import type { MessageContextResolver } from './messageContextResolver'
import type { ProviderSettingChangeHandler } from './providerSettings'
import type { AgentProvider } from '~/generated/proto/leapmux/v1/agent_pb'
import type { AsyncLocalKey } from '~/lib/browserStorage'
import type { ControlRequest } from '~/stores/control.store'
import { createEffect, createMemo, on } from 'solid-js'
import { showWarnToast } from '~/components/common/Toast'
import { AgentActivityState } from '~/generated/proto/leapmux/v1/agent_pb'
import { localStorageDrop, localStorageLoad, localStorageStore, PREFIX_CONTROL_STATE } from '~/lib/browserStorage'
import { clearDraft } from '~/lib/editor/draftPersistence'
import { activityInterruptsWork } from '~/stores/agentActivity.store'
import { controlRequestProvider, requestInstanceId } from '~/stores/control.store'
import { trySubmitAskUserQuestion } from './controls/AskUserQuestionControl'
import { ReportedControlResponseError } from './controls/controlResponseError'
import { canAnswerControlRequest, canSendControlResponse } from './controls/controlResponseState'
import { controlSurface, createControlSurface } from './controls/controlSurface'
import { decidePlanModeToggle } from './planModeToggle'
import { pluginFor } from './providers/registry'
import './providers'

export interface ControlResponseHandlingProps {
  agentId: string
  agent?: { optionValues?: Record<string, string>, agentProvider?: AgentProvider }
  controlRequests?: ControlRequest[]
  messageContext?: MessageContextResolver
  onControlResponse?: ControlResponseHandler
  onSettingChange?: ProviderSettingChangeHandler
  onSendMessage: (content: string, attachments?: FileAttachment[]) => void | Promise<void>
  onSendControlFeedback?: (content: string) => void | Promise<void>
  settingsLoading?: boolean
  /**
   * The activity level the Worker last published for this agent. `showInterrupt`
   * reads it; see there for why the level and not a boolean.
   */
  agentActivity?: AgentActivityState
  /**
   * Whether Interrupt can target THIS agent alone. False for a subagent tab
   * whose provider cannot interrupt one subagent (the worker would answer
   * FailedPrecondition), so the button is not offered at all. Defaults to true.
   */
  canInterrupt?: boolean
}

export interface ControlResponseHandlingResult {
  activeControlRequest: Accessor<ControlRequest | null>
  /**
   * The provider whose plugin renders the active request, and which surface
   * answers it.
   *
   * Both halves of the banner take these as props rather than deriving them,
   * so the composer and the banner cannot reach different answers for the same
   * request, and one graph serves all three readers. See
   * `createControlSurface`.
   */
  activeControlProvider: Accessor<AgentProvider | undefined>
  activeControlSurface: Accessor<ControlSurface | undefined>
  isAskUserQuestion: Accessor<boolean>
  editorPlaceholder: Accessor<string | undefined>
  editorPurpose: Accessor<'answer' | 'feedback' | 'none' | undefined>
  showInterrupt: Accessor<boolean>
  handleControlSend: (content: string) => boolean | void | Promise<boolean | void>
  handleSend: (content: string) => boolean | void | Promise<boolean | void>
  /** Builds the responder that answers as ONE request instance. See `respondTo` below. */
  respondTo: (request: ControlRequest) => ControlResponseSender
  recordResponse: (request: ControlRequest) => Promise<void>
  togglePlanMode: () => void
}

/**
 * Whether `value` is the empty answer a request opens with.
 *
 * The one write the restore path must not make is this exact value, so telling
 * it apart is what lets everything else through.
 */
function isBlankAnswer(value: ControlAnswerSeed): boolean {
  return (value.currentPage ?? 0) === 0
    && Object.keys(value.selections ?? {}).length === 0
    && Object.keys(value.customTexts ?? {}).length === 0
    && Object.keys(value.switches ?? {}).length === 0
    && Object.keys(value.choices ?? {}).length === 0
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
  // Which provider renders the active request, and which surface answers it.
  // Derived ONCE here, for the composer and for both halves of the banner; see
  // `createControlSurface`.
  const { provider: activeProvider, surface: activeSurface } = createControlSurface(
    activeControlRequest,
    () => props.messageContext,
    () => props.agent?.agentProvider,
  )

  /**
   * Which surface answers ONE request instance.
   *
   * The memo above answers for the ACTIVE instance, which it classified with
   * that instance's loaded source message. A caller that holds an instance the
   * store already replaced gets a fresh classification with no source, which is
   * all that instance ever had. Either way `controlSurface` is the one
   * classifier, so the composer and the banner cannot disagree about one
   * request.
   */
  const surfaceFor = (request: ControlRequest): ControlSurface | undefined => {
    const active = activeControlRequest()
    if (active?.agentId === request.agentId && requestInstanceId(active) === requestInstanceId(request))
      return activeSurface()
    return controlSurface(request, controlRequestProvider(request, props.agent?.agentProvider), undefined)
  }
  const questionFor = (request: ControlRequest) => {
    const surface = surfaceFor(request)
    return surface?.kind === 'question' ? surface.question : undefined
  }

  const isAskUserQuestion = createMemo(() => activeSurface()?.kind === 'question')
  const editorPurpose = createMemo(() => {
    const request = activeControlRequest()
    if (!request)
      return undefined
    if (!canAnswerControlRequest(request))
      return 'none'
    // A payload LeapMux could not read composes no answer, and a rejection reason is
    // still an answer: it builds a DENY out of the option list the payload was carrying.
    // The banner offers no decision for this request either, and the stop is the way out.
    if (request.payloadFault)
      return 'none'
    switch (activeSurface()?.kind) {
      // The question form takes the typed answer.
      case 'question':
        return 'answer'
      // The elicitation form carries its own fields, so the editor adds nothing.
      case 'elicitation':
        return 'none'
      default:
        return pluginFor(activeProvider())?.controlEditorPurpose?.(request.payload) ?? 'feedback'
    }
  })
  const editorPlaceholder = createMemo(() => {
    switch (editorPurpose()) {
      case 'answer': return 'Type a custom answer...'
      case 'feedback': return 'Type a rejection reason...'
      default: return undefined
    }
  })

  // Whether the Interrupt button should be shown: the Worker says there is
  // something to stop, and this agent is one the stop can target.
  //
  // The Worker's level is the ONLY input, because the Worker owns every input the
  // answer is made of -- the provider's turn bookkeeping, the background-task
  // registry, the pending prompts, the process state. `activityInterruptsWork` is
  // the same rule the close guard applies, and it covers both halves:
  //
  //   - WORKING. A turn, or a background task, runs.
  //   - WAITING_FOR_USER. The agent is BLOCKED on a prompt, mid-turn. That is
  //     exactly when a reader most wants out, and hiding the button there left one
  //     way out -- answer the question. Denying is a real answer that goes to the
  //     agent and lets it carry on with something else; it is not a stop.
  //
  // A live request on its own is NOT enough, so this reads no request list. A
  // background shell can ask for permission after its agent's turn ended, and the
  // Worker calls that IDLE: there is no turn left, so a button offered on the
  // request alone offers a stop that stops nothing.
  const showInterrupt = () =>
    activityInterruptsWork(props.agentActivity ?? AgentActivityState.IDLE) && (props.canInterrupt ?? true)

  // The saved answer of ONE request instance -- its selections, its typed notes,
  // its page and its switches. `requestInstanceId` states why a request id alone
  // is not enough.
  const answerKey = (request: ControlRequest): AsyncLocalKey =>
    `${PREFIX_CONTROL_STATE}${request.agentId}:${requestInstanceId(request)}`

  // The request whose answer `answerState` holds right now. The restore effect
  // below assigns it; the persist effect reads it. See both for why.
  let answerOwner: ControlRequest | null = null

  // The request a restore is currently READING for, or null when none is. The
  // persist effect skips ONE write while it is set; see both effects for why.
  let restoringFor: ControlRequest | null = null

  // Bumped per restore, so a read that resolves after a newer one started
  // neither applies its answers nor clears the newer run's `restoringFor`.
  let restoreToken = 0

  /** The answer state as one record, which is also what gets persisted. */
  const currentAnswer = (): ControlAnswerSeed => ({
    selections: answerState.selections(),
    customTexts: answerState.customTexts(),
    currentPage: answerState.currentPage(),
    switches: answerState.switches(),
    choices: answerState.choices(),
  })

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
      answerState.setResponsePending(false)
      answerState.setResponseError('')
      answerState.setReady(!request || !props.agentId)
      // RESET FIRST, then fill in from storage if there is anything to fill in.
      // The read is asynchronous (saved answers are an unbounded family on the
      // unmirrored storage tier), and leaving the previous request's answers on
      // screen for the length of that read is what would let a user submit them
      // against the new prompt.
      //
      // `restoringFor` suppresses the persist effect below for the length of
      // that read, and it is not tidiness. Without it the reset itself is a
      // change, so the persist effect writes an EMPTY answer set under this
      // request's key -- and if that write commits before the read lands, the
      // read returns the empty set and the user's saved answers are gone. The
      // window is small and the loss is total, which is the worst shape a race
      // can have.
      const token = ++restoreToken
      restoringFor = request
      answerState.setSelections({})
      answerState.setCustomTexts({})
      answerState.setCurrentPage(0)
      answerState.setSwitches({})
      answerState.setChoices({})
      if (!request || !props.agentId) {
        restoringFor = null
        return
      }
      void localStorageLoad<ControlAnswerSeed>(answerKey(request)).then((saved) => {
        // A newer request started restoring while this read was in flight.
        // Applying now would seed the incoming prompt with the outgoing one's
        // answers -- the same confusion the owner variable below exists to
        // prevent, reached from the read side. The newer run owns
        // `restoringFor`, so this one must not clear it.
        if (token !== restoreToken)
          return
        restoringFor = null
        // The user can answer while the read is in flight. Their in-memory
        // value is newer, so the saved copy loses. Otherwise, install every
        // saved field before the actions become available.
        if (saved && isBlankAnswer(currentAnswer())) {
          answerState.setSelections(saved.selections ?? {})
          answerState.setCustomTexts(saved.customTexts ?? {})
          answerState.setCurrentPage(saved.currentPage ?? 0)
          answerState.setSwitches(saved.switches ?? {})
          answerState.setChoices(saved.choices ?? {})
        }
        answerState.setReady(true)
      }).catch(() => {
        if (token !== restoreToken)
          return
        restoringFor = null
        answerState.setReady(true)
      })
    },
  ))

  // Persist the answers under the key of the request they BELONG to.
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
    const value = currentAnswer()
    const owner = answerOwner
    // EVERY control request, not only a question. A permission prompt and a plan
    // approval carry switches, and those are exactly what a rebuild discards.
    if (!owner || !props.agentId)
      return
    // Skip exactly ONE write: the BLANK answer the restore installs on its way
    // in, while its own read is still in flight. Storing that would destroy the
    // saved copy the read is about to return -- and if the write commits first,
    // the read returns the blank one and the user's answers are gone for good.
    //
    // Narrowed to the blank value rather than to "a restore is running",
    // because the user can answer during that window and what they do must
    // still be saved. The restore checks the same predicate before applying, so
    // whichever of the two lands second defers to the newer state.
    if (restoringFor === owner && isBlankAnswer(value))
      return
    localStorageStore(answerKey(owner), value)
  })

  // Discards every draft of ONE answered request: its editor text, its per-page
  // question answers, and its saved selection state.
  const cleanupControlRequestDrafts = (request: ControlRequest) => {
    if (!request.agentId)
      return
    const instanceId = requestInstanceId(request)
    clearDraft(`${request.agentId}-ctrl-${instanceId}`)
    // The editor scopes a question's draft per page, and it writes one key per
    // question. The same classifier that decides those pages counts them here,
    // so a question set of any size loses every key it wrote. A request that is
    // not a question wrote none, and the loop then does no work.
    const pages = questionFor(request)?.questions.length ?? 0
    for (let page = 0; page < pages; page++) {
      clearDraft(`${request.agentId}-ctrl-${instanceId}-q-${page}`)
    }
    localStorageDrop(answerKey(request))
    // Release the ownership too. The persist effect is deferred, so it runs
    // AFTER this cleanup: `trySubmitAskUserQuestion` writes `answerState` on its
    // way in, and the effect would then re-write the key that the line above
    // just deleted. The answered instance's answers would outlive it. The
    // restore effect assigns the next owner when the head changes.
    if (answerOwner === request)
      answerOwner = null
  }

  // Capture the request instance before delivery. Never read a different request from the store to send an answer.
  // Keep its drafts until the server accepts the answer, so a failed delivery permits retry and reload.
  const pendingResponses = new Set<string>()
  const responseIsCurrent = (request: ControlRequest): boolean => {
    const active = activeControlRequest()
    return props.agentId === request.agentId && active?.agentId === request.agentId && requestInstanceId(active) === requestInstanceId(request)
  }
  /**
   * Delivers ONE answer and reports whether the worker COMPLETED it.
   *
   * `false` means the worker took the bytes but left the request open -- it
   * recorded the response, or it could not confirm delivery. Nothing is cleaned
   * up then: the drafts, the saved answers and the editor text all stay, so the
   * user can send again. A caller that clears the composer must read this.
   */
  const submitResponse = async (request: ControlRequest, bytes: Uint8Array, options?: ControlResponseOptions): Promise<boolean> => {
    const key = JSON.stringify([request.agentId, request.requestId, request.claimToken ?? ''])
    if (pendingResponses.has(key))
      throw new ReportedControlResponseError(new Error('This request already has a pending response.'))
    pendingResponses.add(key)
    if (responseIsCurrent(request)) {
      answerState.setResponsePending(true)
      answerState.setResponseError('')
    }
    try {
      if (!props.onControlResponse)
        throw new Error('The response handler is unavailable.')
      if (!options?.recordOnly && !canSendControlResponse(request))
        throw new Error('Check the saved response state before sending an answer.')
      const completed = options
        ? await props.onControlResponse(request, bytes, options)
        : await props.onControlResponse(request, bytes)
      if (completed === false)
        return false
      cleanupControlRequestDrafts(request)
      const active = activeControlRequest()
      if (props.agentId === request.agentId && (!active || requestInstanceId(active) === requestInstanceId(request)))
        resetEditorHeightFn()
      return true
    }
    catch (cause) {
      const error = new ReportedControlResponseError(cause)
      if (responseIsCurrent(request))
        answerState.setResponseError(error.message)
      else
        showWarnToast('Could not complete the response', cause)
      throw error
    }
    finally {
      pendingResponses.delete(key)
      if (responseIsCurrent(request))
        answerState.setResponsePending(false)
    }
  }

  /**
   * The sender that answers as ONE request instance.
   *
   * It DISCARDS the completion flag, because `ControlResponseSender` is what
   * every provider's `ControlActions` takes and none of them acts on the flag.
   * `handleControlSend` is the one caller that must act on it, and it builds its
   * own sender over `submitResponse` below.
   */
  const respondTo = (request: ControlRequest): ControlResponseSender => async (bytes, options) => {
    await submitResponse(request, bytes, options)
  }
  const recordResponse = async (request: ControlRequest): Promise<void> => {
    await submitResponse(request, new Uint8Array(), { recordOnly: true })
  }

  const handleControlSend = (content: string): boolean | void | Promise<boolean | void> => {
    if (editorPurpose() === 'none' || answerState.responsePending() || !canSendControlResponse(activeControlRequest()))
      return false
    const req = activeControlRequest()
    if (!req)
      return
    if (!answerState.ready())
      return false
    // Whether the worker COMPLETED the answer this send delivers.
    //
    // The editor clears the composer for a `true` and keeps the draft for
    // anything else, so a response the worker only RECORDED does not discard
    // text the user still has to send. The flag is a captured variable rather
    // than a return value: a provider's own sender sits between this function
    // and `submitResponse`, and `ControlResponseSender` carries nothing back.
    // It starts `false`, so a path that never sends keeps the draft.
    let completed = false
    const respond: ControlResponseSender = async (bytes, options) => {
      completed = await submitResponse(req, bytes, options)
    }
    // Resolve the agent's own provider plugin -- no Claude fallback. A live agent
    // always carries a real provider, so a missing plugin means an UNSPECIFIED or
    // unregistered provider (a bug, e.g. backend/frontend version skew). Refuse to
    // encode a control response through the wrong provider's builder; surface a
    // toast so the send is not a silent no-op, and keep the editor content.
    const provider = controlRequestProvider(req, props.agent?.agentProvider)
    const plugin = pluginFor(provider)
    if (!plugin) {
      showWarnToast(`Cannot send response: unsupported agent provider (${provider})`)
      return false
    }
    // Classify the CAPTURED request, not whatever the store holds by now. The
    // shared return type carries the capability, so a question with no capability
    // to answer it cannot be represented here.
    const question = questionFor(req)
    if (question) {
      let sending: Promise<void> | undefined
      const sendAskResponse = () => {
        sending = Promise.resolve(question.capability.sendAnswer(req, respond, question.questions, answerState))
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
      return Promise.resolve(sending).then(() => completed)
    }
    const response = plugin.buildControlResponse?.(req.payload, content, req.requestId)
    if (!response)
      return false
    const bytes = new TextEncoder().encode(JSON.stringify(response))
    const sent = respond(bytes)
    if (content.trim() && plugin.controlFeedbackAsFollowUpMessage?.(req.payload)) {
      return sent.then(async () => {
        // The follow-up message repeats the composer text, so it goes only once
        // the request itself is answered. A recorded-but-open request keeps both.
        if (!completed)
          return false
        await (props.onSendControlFeedback ?? props.onSendMessage)(content)
        return true
      })
    }
    return sent.then(() => completed)
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
    activeControlProvider: activeProvider,
    activeControlSurface: activeSurface,
    handleControlSend,
    handleSend,
    isAskUserQuestion,
    editorPlaceholder,
    editorPurpose,
    respondTo,
    recordResponse,
    showInterrupt,
    togglePlanMode,
  }
}
