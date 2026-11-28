import type { Accessor, Setter } from 'solid-js'
import type { ControlPrompt } from '../model/controlPrompt'
import type { QuestionOption } from '../model/question'
import type { PermissionPresetController } from '../providerSettings'
import type { ControlDecision } from '~/generated/proto/leapmux/v1/agent_control_pb'
import type { AgentProvider } from '~/generated/proto/leapmux/v1/agent_pb'
import type { ContextUsageInfo } from '~/models/agentSession'
import type { ControlRequest } from '~/stores/control.store'
import { createSignal } from 'solid-js'

/**
 * ONE answer that the composer delivers for a request instance:
 *
 *   - `decision`: what the reader chose. The Worker's provider builds the runtime's own
 *     answer from it (AnswerControl).
 *   - `record`: save a response that the Worker already holds, or check its state,
 *     for a request whose answer is still in flight.
 */
export type ControlAnswer
  = | { kind: 'decision', decision: ControlDecision }
    | { kind: 'record' }

/**
 * Delivers ONE answer for a request instance, and resolves true when the Worker
 * completed it. Anything else leaves the request open, so the composer keeps the
 * reader's drafts.
 */
export type ControlAnswerHandler = (request: ControlRequest, answer: ControlAnswer) => Promise<boolean | void>

/** Sends ONE decision for the request that the caller holds. */
export type ControlDecisionSender = (decision: ControlDecision) => Promise<void>

export function questionOptionValue(option: QuestionOption): string {
  return option.value ?? option.label
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
}

export interface ActionsProps {
  request: ControlRequest
  answerState: ControlAnswerState
  /**
   * Sends ONE decision for {@link request}.
   *
   * It takes no agent id: the composer builds this from the request instance the
   * user is answering, and that request already carries the agent, the request
   * id and the per-instance claim token. An id passed alongside could name a
   * different instance, so the parameter is not offered.
   */
  onDecide: ControlDecisionSender
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
  /**
   * The provider of the request, resolved against the agent's (`controlRequestProvider`).
   * A plan approval reads it for the share of the context window that its Clear Context
   * switch states. `undefined` while the provider is not known yet, because a caller
   * passes it as a reactive getter.
   */
  agentProvider?: AgentProvider | undefined
  /**
   * The provider's usable permission presets and the ONE handler that applies a
   * settings change. A control request's permission pill group is drawn from this;
   * selecting a preset applies it when the request's positive action is taken.
   */
  presets?: PermissionPresetController
  contextUsage?: ContextUsageInfo
  modelContextWindow?: number
}

/**
 * What both halves of the banner take, beyond the props of a control.
 *
 * The banner classifies nothing: the Worker states the prompt, and the composer
 * hands the same prompt to the content and to the actions.
 */
interface BannerProps {
  /**
   * What the request asks: the prompt that the Worker read from it.
   *
   * `undefined` for an absent request. The caller must pass the prompt of the
   * request in {@link BannerContentProps.request}, and the composer does: both
   * come from the one active request.
   */
  controlSurface: ControlPrompt | undefined
}

/**
 * The banner's own prop types, which admit an ABSENT request.
 *
 * A control under the banner takes `ContentProps` / `ActionsProps` and reads
 * `request` without a guard, which is correct: the banner renders a control only
 * inside a `<Show>` that already proved the request. The two exported banner
 * components sit one level above that, and a caller CAN pass a request that a
 * store removal turns null -- a reactive prop does exactly that. These types state
 * it, so the compiler requires the guard instead of a reader trusting that one is
 * present.
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
  /**
   * Closes the question and restores the normal composer, leaving the question
   * to wait. Set only for a non-blocking request.
   */
  onAnswerLater?: () => void
}

export interface BannerActionsProps extends Omit<ActionsProps, 'request'>, BannerProps {
  onRecordResponse?: () => Promise<void>
  request: ControlRequest | null
}
