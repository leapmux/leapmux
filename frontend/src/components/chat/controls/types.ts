import type { Accessor, Setter } from 'solid-js'
import type { BypassController } from '../providerSettings'
import type { AgentProvider } from '~/generated/proto/leapmux/v1/agent_pb'
import type { ContextUsageInfo } from '~/stores/agentSession.store'
import type { ControlRequest } from '~/stores/control.store'
import { createSignal } from 'solid-js'

interface QuestionOption {
  id?: string
  label: string
  description?: string
}

export interface Question {
  id?: string
  question: string
  header?: string
  options: QuestionOption[]
  multiSelect?: boolean
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
 * (`plan-clear-context-checkbox`, `control-bypass-permissions-checkbox`,
 * `control-remember-checkbox`). One map covers every control rather than a
 * field per switch, so a new switch needs no change here and cannot be the one
 * that a rebuild silently unchecks.
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
}

/** The saved shape of a {@link ControlAnswerState}, as it is stored and restored. */
export interface ControlAnswerSeed {
  selections?: Record<number, string[]>
  customTexts?: Record<number, string>
  currentPage?: number
  switches?: Record<string, boolean>
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
  return {
    selections,
    setSelections,
    customTexts,
    setCustomTexts,
    currentPage,
    setCurrentPage,
    switches,
    setSwitches,
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
}

export interface ActionsProps {
  request: ControlRequest
  answerState: ControlAnswerState
  /**
   * Sends ONE answer for {@link request}.
   *
   * It takes no agent id: the composer builds this from the request instance the
   * user is answering, and that request already carries the agent, the request
   * id and the per-instance claim token. An id passed alongside could name a
   * different instance, so the parameter is not offered.
   */
  onRespond: (content: Uint8Array) => Promise<void>
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
  /** Applies the provider's complete bypass settings change. */
  bypass?: BypassController
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
export interface BannerContentProps extends Omit<ContentProps, 'request'> {
  request: ControlRequest | null
}

export interface BannerActionsProps extends Omit<ActionsProps, 'request'> {
  request: ControlRequest | null
}

export function sendResponse(
  onRespond: (content: Uint8Array) => Promise<void>,
  response: unknown,
): Promise<void> {
  const bytes = new TextEncoder().encode(JSON.stringify(response))
  return onRespond(bytes)
}

/** Builds a JSON-RPC result with the request ID converted to its wire type. */
export function buildJsonRpcResult(requestId: string, result: unknown): Record<string, unknown> {
  return { jsonrpc: '2.0', id: toRpcId(requestId), result }
}

/** Sends a JSON-RPC result with the request ID converted to its wire type. */
export function sendJsonRpcResult(
  onRespond: (content: Uint8Array) => Promise<void>,
  requestId: string,
  result: unknown,
): Promise<void> {
  return sendResponse(onRespond, buildJsonRpcResult(requestId, result))
}

/** Convert a string request ID to a numeric JSON-RPC id when possible. */
export function toRpcId(requestId: string): number | string {
  const numId = Number(requestId)
  return Number.isFinite(numId) ? numId : requestId
}
