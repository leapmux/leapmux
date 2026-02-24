import type { Accessor, JSX, Setter } from 'solid-js'
import type { ControlRequest } from '~/stores/control.store'
import type { PermissionMode } from '~/utils/controlResponse'

interface QuestionOption {
  label: string
  description?: string
}

export interface Question {
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
}

export interface ActionsProps {
  request: ControlRequest
  askState: AskQuestionState
  onRespond: (agentId: string, content: Uint8Array) => Promise<void>
  hasEditorContent: boolean
  onTriggerSend: () => void
  editorContentRef?: EditorContentRef
  /** Optional info trigger element (context usage icon) to render in the left section. */
  infoTrigger?: JSX.Element
  /** Optional callback to change the agent's permission mode. */
  onPermissionModeChange?: (mode: PermissionMode) => void
}

export function sendResponse(
  agentId: string,
  onRespond: (agentId: string, content: Uint8Array) => Promise<void>,
  response: Record<string, unknown>,
): Promise<void> {
  const bytes = new TextEncoder().encode(JSON.stringify(response))
  return onRespond(agentId, bytes)
}
