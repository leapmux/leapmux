import type { LucideIcon } from 'lucide-solid'
import type { JSX } from 'solid-js'
import type { MessageUiKey } from './messageUiKeys'
import type { ControlResponseSummary } from './model/controlResponse'
import type { UserMessageAttachment } from './model/row'
import type { ExpandableMarkdownRenderContext, MarkdownRenderContext, MessageUiRenderContext } from './renderContext'
import Braces from 'lucide-solid/icons/braces'
import Brain from 'lucide-solid/icons/brain'
import ChevronRight from 'lucide-solid/icons/chevron-right'
import FileIcon from 'lucide-solid/icons/file'
import FileImageIcon from 'lucide-solid/icons/file-image'
import PlaneTakeoff from 'lucide-solid/icons/plane-takeoff'
import { createMemo, createSignal, For, Show, untrack } from 'solid-js'
import { Icon } from '~/components/common/Icon'
import { Tooltip } from '~/components/common/Tooltip'
import { cachedInnerHtml } from '~/lib/htmlFragmentCache'
import { prettifyJson } from '~/lib/jsonFormat'
import { inlineFlex } from '~/styles/shared.css'
import { markdownContent } from './markdownEditor/markdownContent.css'
import { renderMarkdownForContext } from './markdownRendering'
import { attachmentItem, attachmentList, controlResponseLabel, controlResponseMessage, thinkingChevron, thinkingChevronExpanded, thinkingContent, thinkingHeader } from './messageStyles.css'
import { MESSAGE_UI_KEY, messageUiDefault } from './messageUiKeys'
import { CONTROL_RESPONSE_FEEDBACK_LEAD } from './persistedControlResponse'
import {
  toolInputText,
  toolResultContentPre,
  toolUseIcon,
} from './toolStyles.css'
import { ToolUseLayout } from './widgets/ToolUseLayout'

export { markdownCacheNamespace, renderMarkdownForContext, shouldPauseSyntaxHighlighting } from './markdownRendering'

/**
 * Read the parent-driven tool-result-expanded flag from a render context.
 * Centralizes the `?.() ?? false` boilerplate every shared result body needs.
 */
export function getExpandedForKey(context: MessageUiRenderContext | undefined, key: MessageUiKey): boolean {
  const expandAgentThoughts = context?.expandAgentThoughts
  return context?.getMessageUiState?.(key)
    ?? messageUiDefault(key, expandAgentThoughts !== undefined ? { expandAgentThoughts } : {})
}

export function getToolResultExpanded(context: MessageUiRenderContext | undefined): boolean {
  return getExpandedForKey(context, MESSAGE_UI_KEY.TOOL_RESULT_EXPANDED)
}

export function useSharedExpandedState(
  getContext: () => MessageUiRenderContext | undefined,
  key: MessageUiKey,
  // Defaults to the key's shared MESSAGE_UI_DEFAULTS entry (resolved against the
  // context's expandAgentThoughts pref); a renderer with a per-row default passes
  // its own thunk to override it.
  initial: () => boolean = () => {
    const expandAgentThoughts = getContext()?.expandAgentThoughts
    return messageUiDefault(key, expandAgentThoughts !== undefined ? { expandAgentThoughts } : {})
  },
): [() => boolean, (value: boolean | ((prev: boolean) => boolean)) => void] {
  const [localExpanded, setLocalExpanded] = createSignal<boolean | undefined>(undefined)
  const expanded = () => getContext()?.getMessageUiState?.(key) ?? localExpanded() ?? initial()
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

/**
 * Render markdown text via the shared remark pipeline. The HTML is produced
 * via remark + sanitizer, never arbitrary user input. It is applied through
 * the parsed-fragment cache (~/lib/htmlFragmentCache) rather than an
 * `innerHTML` binding, so a re-mounting row clones the already-parsed
 * template instead of making the browser re-parse the same markup.
 */
export function MarkdownText(props: { text: string, context?: MarkdownRenderContext }): JSX.Element {
  const html = createMemo(() => renderMarkdownForContext(props.text, props.context))
  return <div class={markdownContent} ref={cachedInnerHtml(html)} />
}

type ThinkingBubbleProps = {
  icon: LucideIcon
  label: string
  stateKey: MessageUiKey
  context?: ExpandableMarkdownRenderContext
} & (
  | { text: string, renderBody?: never }
  | { text?: never, renderBody: () => JSX.Element }
)

/** Shared assistant thinking/reasoning bubble with chevron-controlled body. */
export function ThinkingBubble(props: ThinkingBubbleProps): JSX.Element {
  const stateKey = untrack(() => props.stateKey)
  // The default-expanded value comes from the stateKey's MESSAGE_UI_DEFAULTS entry
  // (THINKING follows expandAgentThoughts; PLAN_EXECUTION collapses) via
  // useSharedExpandedState, so renderer defaults stay centralized.
  const [expanded, setExpanded] = useSharedExpandedState(() => props.context, stateKey)
  const body = (): JSX.Element => {
    if (props.renderBody)
      return props.renderBody()
    // The props union pairs `text` with an absent `renderBody`, so the body has
    // its text whenever this line runs.
    const text = props.text
    return text !== undefined
      ? <MarkdownText text={text} {...(props.context !== undefined ? { context: props.context } : {})} />
      : null
  }

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
          {body()}
        </div>
      </Show>
    </>
  )
}

export function ThinkingMessage(props: { text: string, context?: ExpandableMarkdownRenderContext }): JSX.Element {
  // Key from the shared classification mapper (context.expandUiKey) so it matches
  // the estimator's pre-mount assumption; the literal is the context-less fallback.
  return <ThinkingBubble text={props.text} icon={Brain} label="Thinking" stateKey={props.context?.expandUiKey ?? MESSAGE_UI_KEY.THINKING} {...(props.context !== undefined ? { context: props.context } : {})} />
}

export function PlanExecutionMessage(props: { text: string, context?: ExpandableMarkdownRenderContext }): JSX.Element {
  return <ThinkingBubble text={props.text} icon={PlaneTakeoff} label="Execute plan" stateKey={props.context?.expandUiKey ?? MESSAGE_UI_KEY.PLAN_EXECUTION} {...(props.context !== undefined ? { context: props.context } : {})} />
}

/**
 * Provider-neutral renderer for user messages persisted as
 * `{"content":"...", "attachments":[...]}` by the LeapMux service layer.
 * Used by Claude, Codex, Pi, and every ACP-based provider
 * (OpenCode/Cursor/Goose/Kilo/Copilot/Reasonix) so no plugin has to reinvent
 * attachment + markdown rendering. Renders nothing when the parsed body has
 * no usable text or attachments.
 */
export function UserContentMessage(props: { text: string, attachments: UserMessageAttachment[], context?: MarkdownRenderContext }): JSX.Element {
  const hasText = (): boolean => props.text.trim().length > 0
  const hasAttachments = (): boolean => props.attachments.length > 0
  const hasAny = (): boolean => hasText() || hasAttachments()

  return (
    <Show when={hasAny()}>
      <Show when={hasAttachments()}>
        <div class={attachmentList}>
          <For each={props.attachments}>
            {att => (
              <span class={attachmentItem}>
                <Icon
                  icon={att.mimeType?.startsWith('image/') ? FileImageIcon : FileIcon}
                  size="xs"
                />
                {att.filename ?? 'Unnamed file'}
              </span>
            )}
          </For>
        </div>
      </Show>
      <Show when={hasText()}>
        <MarkdownText text={props.text} {...(props.context !== undefined ? { context: props.context } : {})} />
      </Show>
    </Show>
  )
}

/**
 * Render a persisted control-response row (issue #258).
 *
 * The shared markup for the two display kinds, and nothing else: a feedback block
 * renders the user's typed reason as markdown under the "Sent feedback:" lead, and a
 * label renders line-broken plain text (a multi-question answer joins its lines with
 * `\n`).
 *
 * The native-payload -> label/feedback derivation happens in LAYER 1, where the
 * provider's `controlResponseDisplay` runs once for every reader of the row
 * (~/components/chat/rowExtraction.ts). This renderer therefore takes the display and
 * cannot reach a provider payload at all -- it used to run the derivation itself, so
 * the transcript row and the scroll-rail dot each dispatched through the plugin for
 * one answer, and each carried its own copy of the fallback.
 */
export function renderControlResponseRow(
  display: ControlResponseSummary,
  context: MarkdownRenderContext | undefined,
): JSX.Element {
  if (display.kind === 'feedback') {
    return (
      <div class={controlResponseMessage}>
        <div>
          <div>{CONTROL_RESPONSE_FEEDBACK_LEAD}</div>
          <MarkdownText text={display.message} {...(context !== undefined ? { context } : {})} />
        </div>
      </div>
    )
  }
  return <div class={`${controlResponseMessage} ${controlResponseLabel}`} data-testid="control-response-text">{display.text}</div>
}

/**
 * The row that produced no display.
 *
 * Three things arrive here: a row no plugin claimed, a row a plugin claimed and then
 * drew nothing for -- a `settings_changed` notification with no changes in it -- and a
 * row whose renderer threw. The first two are one statement to the reader, because
 * LeapMux has nothing to show either way. The third is a defect in LeapMux, and calling
 * that "no display" would send the next reader hunting the provider instead.
 *
 * A live census found the first case: GitHub Copilot's `session.task_complete` reached
 * here, and the reader saw the whole JSON-RPC frame as a paragraph of text.
 *
 * The frame itself stays, because it is the only content this row has and hiding it
 * would lose whatever the provider did send. It starts COLLAPSED: an unrecognized row
 * is nearly always a frame the transcript has no use for, and the reader who wants it
 * is one click away. The toolbar's own Copy JSON action holds the same bytes.
 *
 * A plugin that can name its own unrecognized rows does not need this: `renderMessage`
 * already receives the `unknown` kind, so a provider renders its own card there and
 * never reaches this one. This card therefore reads NOTHING out of the payload, which
 * keeps every provider's wire shape inside that provider's plugin.
 *
 * It lives here rather than in `results/` because it is not a tool result. It is the
 * fallback for any row that the extraction layer cannot turn into the local model.
 */
export function UnrecognizedMessage(props: {
  payload: unknown
  /** True when a renderer threw for this row, rather than no renderer claiming it. */
  renderFailed?: boolean
  context?: MessageUiRenderContext
}): JSX.Element {
  const text = (): string => typeof props.payload === 'string' ? props.payload : prettifyJson(props.payload)
  const title = (): string => props.renderFailed
    ? 'LeapMux could not render this row'
    : 'LeapMux has no display for this row'
  const [expanded, setExpanded] = useSharedExpandedState(() => props.context, MESSAGE_UI_KEY.UNRECOGNIZED_ROW)

  // The layout gets NO context on purpose. It would draw a second Copy JSON action from
  // it, and the message host already supplies that one in the bubble's own toolbar --
  // two controls with one test id, and the reader with two buttons that do one thing.
  // The expand control stays, because the layout draws that from `onToggleExpand`.
  return (
    <ToolUseLayout
      icon={Braces}
      toolName="Unrecognized row"
      title={title()}
      expanded={expanded()}
      onToggleExpand={() => setExpanded(v => !v)}
    >
      <div class={toolResultContentPre}>{text()}</div>
    </ToolUseLayout>
  )
}
