import type { Accessor, Setter } from 'solid-js'
import type { BypassController } from '../providerSettings'
import type { AgentProvider } from '~/generated/proto/leapmux/v1/agent_pb'
import type { ContextUsageInfo } from '~/stores/agentSession.store'
import type { ControlRequest } from '~/stores/control.store'

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

/** Shared state for AskUserQuestion selections, lifted to parent for split rendering. */
export interface AskQuestionState {
  selections: Accessor<Record<number, string[]>>
  setSelections: Setter<Record<number, string[]>>
  customTexts: Accessor<Record<number, string>>
  setCustomTexts: Setter<Record<number, string>>
  currentPage: Accessor<number>
  setCurrentPage: Setter<number>
}

/** Ref object for getting/setting editor content programmatically. */
export interface EditorContentRef {
  get: () => string
  set: (text: string) => void
}

export interface ContentProps {
  request: ControlRequest
  askState: AskQuestionState
  optionsDisabled?: boolean
  agentProvider?: AgentProvider
}

export interface ActionsProps {
  request: ControlRequest
  askState: AskQuestionState
  onRespond: (agentId: string, content: Uint8Array) => Promise<void>
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

export function sendResponse(
  agentId: string,
  onRespond: (agentId: string, content: Uint8Array) => Promise<void>,
  response: unknown,
): Promise<void> {
  const bytes = new TextEncoder().encode(JSON.stringify(response))
  return onRespond(agentId, bytes)
}

/** Builds a JSON-RPC result with the request ID converted to its wire type. */
export function buildJsonRpcResult(requestId: string, result: unknown): Record<string, unknown> {
  return { jsonrpc: '2.0', id: toRpcId(requestId), result }
}

/** Sends a JSON-RPC result with the request ID converted to its wire type. */
export function sendJsonRpcResult(
  agentId: string,
  onRespond: (agentId: string, content: Uint8Array) => Promise<void>,
  requestId: string,
  result: unknown,
): Promise<void> {
  return sendResponse(agentId, onRespond, buildJsonRpcResult(requestId, result))
}

/** Convert a string request ID to a numeric JSON-RPC id when possible. */
export function toRpcId(requestId: string): number | string {
  const numId = Number(requestId)
  return Number.isFinite(numId) ? numId : requestId
}
