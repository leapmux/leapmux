import type { Accessor, Setter } from 'solid-js'
import type { MessageContextResolver } from '../messageContextResolver'
import type { PermissionPresetController } from '../providerSettings'
import type { ControlSurface } from './controlSurface'
import type { AgentProvider, PlanApprovalSettings } from '~/generated/proto/leapmux/v1/agent_pb'
import type { ContextUsageInfo } from '~/stores/agentSession.store'
import type { ControlRequest } from '~/stores/control.store'
import { createSignal } from 'solid-js'

export interface ControlResponseOptions {
  recordOnly?: boolean
  planApproval?: Pick<PlanApprovalSettings, 'permissionMode' | 'clearContext'>
}

export type ControlResponseHandler = (request: ControlRequest, content: Uint8Array, options?: ControlResponseOptions) => Promise<boolean | void>

export type ControlResponseSender = (content: Uint8Array, options?: ControlResponseOptions) => Promise<void>

export interface QuestionOption {
  /** The response value stays stable when supplemental data changes the label. */
  value?: string
  label: string
  description?: string
  preview?: string
}

export function questionOptionValue(option: QuestionOption): string {
  return option.value ?? option.label
}

export interface Question {
  id?: string
  question: string
  header?: string
  options: QuestionOption[]
  multiSelect?: boolean
  /** The provider accepts an explicit empty answer. */
  allowEmpty?: boolean
}

/**
 * The user's in-progress answer to ONE control request.
 *
 * It is lifted to the composer, not held inside the control component, so that a
 * rebuild of that component cannot discard it. `controlResponseHandling`
 * persists the whole record per request INSTANCE and restores it, so a remount
 * or a reload brings the answer back.
 *
 * `switches` holds every toggle a control offers, by the switch's own id
 * (`plan-clear-context-checkbox`). One map covers every control rather than a
 * field per switch, so a new switch needs no change here and cannot be the one
 * that a rebuild silently unchecks. `choices` is its one-of-N sibling, holding
 * a pill group's selection by the group's id (`control-permissions-pill`,
 * `control-allow-choice-pill`) — a string, because a pill picks a key, not a
 * boolean.
 */
export interface ControlAnswerState {
  selections: Accessor<Record<number, string[]>>
  setSelections: Setter<Record<number, string[]>>
  customTexts: Accessor<Record<number, string>>
  setCustomTexts: Setter<Record<number, string>>
  currentPage: Accessor<number>
  setCurrentPage: Setter<number>
  switches: Accessor<Record<string, boolean>>
  setSwitches: Setter<Record<string, boolean>>
  choices: Accessor<Record<string, string>>
  setChoices: Setter<Record<string, string>>
  /** Whether the active request's persisted answer is ready to use. */
  ready: Accessor<boolean>
  setReady: Setter<boolean>
  responsePending: Accessor<boolean>
  setResponsePending: Setter<boolean>
  responseError: Accessor<string>
  setResponseError: Setter<string>
}

/** The saved shape of a {@link ControlAnswerState}, as it is stored and restored. */
export interface ControlAnswerSeed {
  selections?: Record<number, string[]>
  customTexts?: Record<number, string>
  currentPage?: number
  switches?: Record<string, boolean>
  choices?: Record<string, string>
}

/**
 * Builds a {@link ControlAnswerState}, optionally seeded from a saved record.
 *
 * The one constructor in the repo, so a field added to the interface above
 * cannot be forgotten at a second assembly site.
 */
export function createControlAnswerState(seed: ControlAnswerSeed = {}): ControlAnswerState {
  const [selections, setSelections] = createSignal(seed.selections ?? {})
  const [customTexts, setCustomTexts] = createSignal(seed.customTexts ?? {})
  const [currentPage, setCurrentPage] = createSignal(seed.currentPage ?? 0)
  const [switches, setSwitches] = createSignal(seed.switches ?? {})
  const [choices, setChoices] = createSignal(seed.choices ?? {})
  const [ready, setReady] = createSignal(true)
  const [responsePending, setResponsePending] = createSignal(false)
  const [responseError, setResponseError] = createSignal('')
  return {
    selections,
    setSelections,
    customTexts,
    setCustomTexts,
    currentPage,
    setCurrentPage,
    switches,
    setSwitches,
    choices,
    setChoices,
    ready,
    setReady,
    responsePending,
    setResponsePending,
    responseError,
    setResponseError,
  }
}

/**
 * Binds ONE switch of a control to the shared answer state, by the switch's own
 * id.
 *
 * Every switch reads and writes here rather than a local signal. A control
 * component is rebuilt whenever the active request changes identity, and the
 * composer itself is rebuilt whenever the focused agent changes, so a local
 * signal loses a choice the user already made and the response then omits it
 * with nothing telling the user. The shared record survives both, and
 * `controlResponseHandling` persists it per request INSTANCE.
 */
export function createControlSwitch(state: () => ControlAnswerState, id: string) {
  // Captured ONCE, at the creation of the control component that owns the
  // switch. The composer holds one answer record for the whole life of that
  // component, so there is nothing to re-read -- and re-reading would be worse
  // than useless: a caller that builds the record inline in JSX
  // (`answerState={createControlAnswerState()}`) makes the prop a getter, and every
  // read would then mint a fresh empty record and lose the user's choice.
  const answer = state()
  return {
    checked: () => answer.switches()[id] ?? false,
    set: (value: boolean) => answer.setSwitches(prev => ({ ...prev, [id]: value })),
  }
}

/**
 * The one-of-N sibling of {@link createControlSwitch}: binds ONE pill group of a
 * control to the shared answer record, by the group's own id. A caller can
 * supply a fallback when its domain has a meaningful unset key. Without one,
 * the choice stays `undefined` until a selection lands.
 */
export function createControlChoice(state: () => ControlAnswerState, id: string, fallback?: string) {
  const answer = state()
  return {
    choice: () => answer.choices()[id] ?? fallback,
    setChoice: (value: string) => answer.setChoices(prev => ({ ...prev, [id]: value })),
  }
}

/** The saved choice key for a control request's allow behavior. */
export const CONTROL_ALLOW_CHOICE_ID = 'control-allow-choice-pill'

/** Ref object for getting/setting editor content programmatically. */
export interface EditorContentRef {
  get: () => string
  set: (text: string) => void
}

export interface ContentProps {
  request: ControlRequest
  answerState: ControlAnswerState
  optionsDisabled?: boolean
  agentProvider?: AgentProvider
  messageContext?: MessageContextResolver
}

export interface ActionsProps {
  request: ControlRequest
  messageContext?: MessageContextResolver
  answerState: ControlAnswerState
  /**
   * Sends ONE answer for {@link request}.
   *
   * It takes no agent id: the composer builds this from the request instance the
   * user is answering, and that request already carries the agent, the request
   * id and the per-instance claim token. An id passed alongside could name a
   * different instance, so the parameter is not offered.
   */
  onRespond: ControlResponseSender
  hasEditorContent: boolean
  onTriggerSend: () => void
  /**
   * The live editor handle, as an ACCESSOR rather than a value.
   *
   * The handle exists after the editor's `contentRef` callback runs, which
   * is AFTER the owner's JSX is created. A plain-valued prop is therefore unusable
   * here in practice: every caller has it in a `let` that is still `undefined` at
   * creation time, and Solid's JSX transform treats a bare identifier prop as
   * STATIC -- it captures that `undefined` and never re-reads it. `AgentEditorPanel`
   * did exactly that, so this arrived permanently unset and the multi-question
   * save/restore below silently did nothing. An accessor cannot be captured stale,
   * which makes the mistake unrepresentable instead of merely fixed once.
   */
  editorContentRef?: () => EditorContentRef | undefined
  agentProvider?: AgentProvider
  /**
   * The provider's usable permission presets and the ONE handler that applies a
   * settings change. A control request's permission pill group is drawn from this;
   * selecting a preset applies it when the request's positive action is taken.
   */
  presets?: PermissionPresetController
  contextUsage?: ContextUsageInfo
  modelContextWindow?: number
  /**
   * Optional pre-extracted question list. Providers whose payload shape
   * isn't compatible with `getToolInput(...).questions` (e.g. Pi's
   * extension_ui_request) pass this directly so AskUserQuestionActions
   * can drive the same selection / multi-page flow without a wrapper
   * adapter.
   */
  questions?: Question[]
}

/**
 * What both halves of the banner take, beyond the props a plugin's control
 * takes.
 *
 * The banner CLASSIFIES nothing. Its caller derives the surface and the
 * provider once, with `createControlSurface`, and hands the same two values to
 * the content and to the actions. Both mount for the SAME request in two
 * different slots, so a banner that classified its own request built that graph
 * twice and the composer built it a third time.
 */
interface BannerProps {
  /**
   * Which surface answers the request: the question form, the elicitation form,
   * or the provider's own plugin.
   *
   * `undefined` for an absent request. The caller must pass the surface of the
   * request in {@link BannerContentProps.request}, and the composer does: both
   * come from the one active request.
   */
  controlSurface: ControlSurface | undefined
  /**
   * The provider whose plugin renders the request, ALREADY resolved against the
   * request's own provider. It is not the agent's provider, which the caller
   * resolves beside the surface so that both answers come from one place.
   */
  agentProvider?: AgentProvider
}

/**
 * The banner's own prop types, which admit an ABSENT request.
 *
 * A provider's `ControlContent` / `ControlActions` takes `ContentProps` /
 * `ActionsProps` and dereferences `request.payload` without a guard, which is
 * correct: the banner renders a plugin only inside a `<Show>` that already
 * proved the request. The two exported banner components sit one level above
 * that, and a caller CAN pass a request that a store removal turns null -- a
 * reactive prop does exactly that. These types state it, so the compiler
 * requires the guard instead of a reader trusting that one is present.
 */
export interface BannerContentProps extends Omit<ContentProps, 'request'>, BannerProps {
  request: ControlRequest | null
  /**
   * Stops the turn the request belongs to. Absent when this agent cannot be
   * interrupted on its own, which is how a subagent tab gets no such control.
   *
   * It lives with the QUESTION rather than with the answers: a stop is not a decision,
   * and a button next to Allow and Deny would read as one.
   */
  onInterrupt?: () => void
}

export interface BannerActionsProps extends Omit<ActionsProps, 'request'>, BannerProps {
  onRecordResponse?: () => Promise<void>
  request: ControlRequest | null
}

export function sendResponse(
  onRespond: ControlResponseSender,
  response: unknown,
  options?: ControlResponseOptions,
): Promise<void> {
  const bytes = new TextEncoder().encode(JSON.stringify(response))
  return options ? onRespond(bytes, options) : onRespond(bytes)
}

/** Keep the worker request ID intact. The worker restores the native ID from the persisted request. */
export function buildJsonRpcResult(requestId: string, result: unknown): Record<string, unknown> {
  return { jsonrpc: '2.0', id: requestId, result }
}

/** Send the result with the unchanged worker request ID. */
export function sendJsonRpcResult(
  onRespond: (content: Uint8Array) => Promise<void>,
  requestId: string,
  result: unknown,
): Promise<void> {
  return sendResponse(onRespond, buildJsonRpcResult(requestId, result))
}

/**
 * Sends the ACP-family `session/request_permission` reply that selects one
 * option. The ACP and OpenCode protocols share this envelope, so both providers'
 * senders delegate here instead of building it twice.
 */
export function sendSelectedOptionResponse(
  onRespond: (content: Uint8Array) => Promise<void>,
  requestId: string,
  optionId: string,
): Promise<void> {
  return sendJsonRpcResult(onRespond, requestId, { outcome: { outcome: 'selected', optionId } })
}
