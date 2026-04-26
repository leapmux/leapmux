import type { LucideIcon } from 'lucide-solid'
import type { JSX } from 'solid-js'
import type { MessageCategory } from './messageClassification'
import type { DiffViewPreference } from '~/context/PreferencesContext'
import type { MessageRole } from '~/generated/leapmux/v1/agent_pb'
import type { ParsedMessageContent } from '~/lib/messageParser'
import type { CommandStreamSegment } from '~/stores/chat.store'
import Brain from 'lucide-solid/icons/brain'
import ChevronRight from 'lucide-solid/icons/chevron-right'
import PlaneTakeoff from 'lucide-solid/icons/plane-takeoff'
import { createSignal, Show, untrack } from 'solid-js'
import { Icon } from '~/components/common/Icon'
import { Tooltip } from '~/components/common/Tooltip'
import { AgentProvider } from '~/generated/leapmux/v1/agent_pb'
import { createLogger } from '~/lib/logger'
import { renderMarkdown } from '~/lib/renderMarkdown'
import { inlineFlex } from '~/styles/shared.css'
import { markdownContent } from './markdownEditor/markdownContent.css'
import { thinkingChevron, thinkingChevronExpanded, thinkingContent, thinkingHeader } from './messageStyles.css'
import { getProviderPlugin } from './providers/registry'
import {
  toolInputText,
  toolUseIcon,
} from './toolStyles.css'
import './providers'

const logger = createLogger('messageRenderers')

/**
 * Context passed to renderers from MessageBubble.
 *
 * Reactive UI state (`toolResultExpanded`, `jsonCopied`, `diffView`) is exposed
 * as getter functions so the context object itself stays referentially stable
 * across re-renders. That lets the renderer functions called from MessageBubble
 * skip re-running on UI toggles — only the body components that actually read
 * the getters re-evaluate.
 */
export interface RenderContext {
  /** ISO timestamp of the message (for relative time in toolbar). */
  createdAt?: string
  workingDir?: string
  /** Worker's home directory for tilde (~) path simplification. */
  homeDir?: string
  /** User's preferred diff view. */
  diffView?: () => DiffViewPreference
  /** Reply/quote callback — inserts quoted text into the editor. */
  onReply?: (quotedText: string) => void
  /** Copy raw JSON to clipboard. */
  onCopyJson?: () => void
  /** Whether JSON was just copied (for feedback). */
  jsonCopied?: () => boolean
  /** Whether thinking/reasoning bubbles should start expanded by default. */
  expandAgentThoughts?: boolean
  /** Parent tool_use name (passed to tool_result renderers for context). */
  parentToolName?: string
  /** Parent tool_use input (passed to tool_result renderers for context). */
  parentToolInput?: Record<string, unknown>
  /** Pre-parsed tool_use message for tool_result bubbles to inspect (cached by the store). */
  toolUseParsed?: ParsedMessageContent
  /** Pre-parsed tool_result message for tool_use bubbles to inspect (cached by the store). */
  toolResultParsed?: ParsedMessageContent
  /** Color index assigned to this message's span (−1 = no color). */
  spanColor?: number
  /** Tool name or item type from span_type column (reliable, always set for span messages). */
  spanType?: string
  /** Current message span id. */
  spanId?: string
  /** Whether the Bash/TaskOutput tool result is expanded (controlled by MessageBubble). */
  toolResultExpanded?: () => boolean
  /** Live streamed Codex span content for command, fileChange, and reasoning items. */
  commandStream?: () => CommandStreamSegment[] | undefined
  /** Stable per-message UI state getter for remount-sensitive renderers. */
  getMessageUiState?: (key: string) => boolean | undefined
  /** Stable per-message UI state setter for remount-sensitive renderers. */
  setMessageUiState?: (key: string, value: boolean) => void
}

export interface MessageContentRenderer {
  /** Try to render the parsed JSON content. Return null if this renderer doesn't handle it. */
  render: (parsed: unknown, role: MessageRole, context?: RenderContext) => JSX.Element | null
}

export function useSharedExpandedState(
  getContext: () => RenderContext | undefined,
  key: string,
  initial: boolean | (() => boolean) = false,
): [() => boolean, (value: boolean | ((prev: boolean) => boolean)) => void] {
  const [localExpanded, setLocalExpanded] = createSignal<boolean | undefined>(undefined)
  const resolveInitial = () => typeof initial === 'function'
    ? (initial as () => boolean)()
    : initial
  const expanded = () => getContext()?.getMessageUiState?.(key) ?? localExpanded() ?? resolveInitial()
  const setExpanded = (value: boolean | ((prev: boolean) => boolean)) => {
    const ctx = getContext()
    const next = typeof value === 'function'
      ? (value as (prev: boolean) => boolean)(expanded())
      : value
    if (ctx?.setMessageUiState)
      ctx.setMessageUiState(key, next)
    else
      setLocalExpanded(next)
  }
  return [expanded, setExpanded]
}

/** Shared assistant thinking/reasoning bubble with chevron-controlled body. */
export function ThinkingBubble(props: {
  text: string
  icon: LucideIcon
  label: string
  stateKey: string
  context?: RenderContext
  defaultExpanded?: boolean
}): JSX.Element {
  const stateKey = untrack(() => props.stateKey)
  const [expanded, setExpanded] = useSharedExpandedState(
    () => props.context,
    stateKey,
    () => props.defaultExpanded ?? props.context?.expandAgentThoughts ?? true,
  )

  return (
    <>
      <div class={thinkingHeader} onClick={() => setExpanded(v => !v)}>
        <Tooltip text={props.label} ariaLabel>
          <span class={inlineFlex}>
            <Icon icon={props.icon} size="md" class={toolUseIcon} />
          </span>
        </Tooltip>
        <span class={toolInputText}>{props.label}</span>
        <span class={`${inlineFlex} ${thinkingChevron}${expanded() ? ` ${thinkingChevronExpanded}` : ''}`}>
          <Icon icon={ChevronRight} size="sm" class={toolUseIcon} />
        </span>
      </div>
      <Show when={expanded()}>
        <div class={thinkingContent}>
          {/* eslint-disable-next-line solid/no-innerhtml -- HTML is produced via remark, not arbitrary user input */}
          <div class={markdownContent} innerHTML={renderMarkdown(props.text)} />
        </div>
      </Show>
    </>
  )
}

export function ThinkingMessage(props: { text: string, context?: RenderContext }): JSX.Element {
  return <ThinkingBubble text={props.text} icon={Brain} label="Thinking" stateKey="thinking" context={props.context} />
}

export function PlanExecutionMessage(props: { text: string, context?: RenderContext }): JSX.Element {
  return <ThinkingBubble text={props.text} icon={PlaneTakeoff} label="Execute plan" stateKey="planExecution" context={props.context} defaultExpanded={false} />
}

/**
 * Render a message's content.
 *
 * All rendering goes through the provider plugin's `renderMessage`. The plugin
 * is responsible for handling every kind it can render, including `'unknown'`
 * (where it runs its own type-detection chain on the parsed object). When
 * `agentProvider` is missing, the dispatch defaults to the Claude plugin —
 * mirroring `classifyMessage`'s registry-fallback in `messageClassification.ts`.
 *
 * Returns a raw-text `<span>` only when no plugin handles the message at all
 * (or when JSON parsing fails) — the absolute last-resort safety net.
 */
export function renderMessageContent(
  parsedOrRawJson: unknown,
  role: MessageRole,
  context?: RenderContext,
  category?: MessageCategory,
  agentProvider?: AgentProvider,
): JSX.Element {
  try {
    const parsed = typeof parsedOrRawJson === 'string'
      ? JSON.parse(parsedOrRawJson)
      : parsedOrRawJson

    const provider = agentProvider ?? AgentProvider.CLAUDE_CODE
    const plugin = getProviderPlugin(provider) ?? getProviderPlugin(AgentProvider.CLAUDE_CODE)
    const result = plugin?.renderMessage?.(category ?? { kind: 'unknown' }, parsed, role, context) ?? null
    if (result !== null)
      return result
  }
  catch (err) { logger.warn('Failed to render message content:', err) }
  return <span>{typeof parsedOrRawJson === 'string' ? parsedOrRawJson : JSON.stringify(parsedOrRawJson)}</span>
}
