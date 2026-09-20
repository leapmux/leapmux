import type { LucideIcon } from 'lucide-solid'
import type { JSX } from 'solid-js'
import type { GenericToolKind } from '../../model/toolCall'
import type { ParsedCall, ResolvedCall, ToolKindMeta, ToolKindRenderer, ToolRowView } from './renderer'
import { Show } from 'solid-js'
import { prettifyJson } from '~/lib/jsonFormat'
import { isObject } from '~/lib/jsonPick'
import { humanizeWireWord } from '../../rendererUtils'
import { toolInputCode, toolInputSummary, toolInputText } from '../../toolStyles.css'
import { CollapsibleContent } from '../CollapsibleContent'
import { genericResultCollapsible, genericResultCopyable, GenericToolBody } from '../genericToolCall'
import { useCollapsedLines } from '../useCollapsedLines'

/** The two kinds one renderer serves: a tool no vocabulary lists, with no protocol shape of its own. */
export type PlainGenericKind = 'unspecified' | 'other'

const INPUT_HINT_KEYS = ['query', 'input', 'prompt', 'text', 'command', 'description', 'url']

function shortInputHint(value: unknown): string {
  return typeof value === 'string' && value.length > 0 && value.length <= 120
    ? value.length > 80 ? `${value.slice(0, 80)}…` : value
    : ''
}

/** Prefer a common argument key, then the first short string. */
export function toolInputHint(input: unknown): string {
  if (!isObject(input))
    return ''
  for (const key of INPUT_HINT_KEYS) {
    const hint = shortInputHint(input[key])
    if (hint)
      return hint
  }
  for (const value of Object.values(input)) {
    const hint = shortInputHint(value)
    if (hint)
      return hint
  }
  return ''
}

/** The title a generic call draws: the tool's own name and the one argument that says what it wanted. */
export function genericTitle(displayName: string, input: unknown): JSX.Element {
  const hint = toolInputHint(input)
  return (
    <>
      <span class={toolInputText}>{displayName}</span>
      <Show when={hint}><span class={toolInputCode}>{` "${hint}"`}</span></Show>
    </>
  )
}

function argsTextOf(args: Record<string, unknown>, argsText?: string): string {
  return argsText ?? (Object.keys(args).length > 0 ? prettifyJson(args) : '')
}

function PendingArguments(props: { text: string, view: ToolRowView }): JSX.Element {
  const collapsed = useCollapsedLines({ text: () => props.text, expanded: () => props.view.expanded() })
  return (
    <>
      <div class={toolInputSummary}>Arguments</div>
      <CollapsibleContent kind="pre" text={props.text} display={collapsed.display()} isCollapsed={collapsed.isCollapsed()} {...(props.view.context !== undefined ? { context: props.view.context } : {})} />
    </>
  )
}

/** The display name of a plain generic call, for the image tabs it opens. */
function plainImageTabName(call: { label?: string, name: string }): string {
  return call.label ?? (call.name ? humanizeWireWord(call.name) : 'Tool')
}

/**
 * The request body every generic kind shares: a pending unpaired row shows what it
 * asked; once a result exists, the body's own Arguments block is the one place.
 */
export function genericRequestBody(call: ParsedCall<GenericToolKind>, view: ToolRowView): JSX.Element | null {
  const text = view.drawsResult && call.result === undefined
    ? argsTextOf(call.request.args, call.request.argsText)
    : ''
  return <Show when={text}>{args => <PendingArguments text={args()} view={view} />}</Show>
}

/** The result body every generic kind shares, headed by the tab name its images open under. */
export function genericResultBody(call: ResolvedCall<GenericToolKind>, view: ToolRowView, imageTabName: string): JSX.Element | null {
  return (
    <GenericToolBody
      request={call.request}
      result={call.result}
      status={call.status}
      {...(view.context?.images !== undefined ? { actions: view.context?.images } : {})}
      holdDisplay={() => view.context?.syntaxHighlightingPaused?.() === true || view.context?.textSelectionActive?.() === true}
      {...(view.context !== undefined ? { context: view.context } : {})}
      expanded={view.expanded}
      indexOffset={view.imageIndexOffset}
      title={imageTabName}
    />
  )
}

/** What the toolbar offers a generic result row, whatever generic kind drew it. */
export function genericResultMeta(call: ResolvedCall<GenericToolKind>): ToolKindMeta {
  const argsJson = argsTextOf(call.request.args, call.request.argsText)
  return {
    collapsible: genericResultCollapsible(call.result, argsJson),
    hasDiff: false,
    copyableContent: () => genericResultCopyable(call.result) || null,
  }
}

/**
 * The renderer the plain generic duo shares: a tool no vocabulary lists, drawn from
 * its arguments and its content blocks alone.
 */
export function genericRenderer(options: { icon: LucideIcon, label: string }): ToolKindRenderer<PlainGenericKind> {
  return {
    icon: options.icon,
    label: options.label,
    nameLeads: true,
    // The one kind whose order differs, and `nameLeads` is the reason: the tool's own
    // NAME heads the row, so `call.title` leads and the request supplies the argument
    // hint beside it. Every other kind puts the request first (see `renderer.ts`).
    title(call: ParsedCall<PlainGenericKind>): JSX.Element | string {
      return genericTitle(call.title ?? (call.name ? humanizeWireWord(call.name) : options.label), call.request.args)
    },
    request: genericRequestBody,
    result(call: ResolvedCall<PlainGenericKind>, view: ToolRowView): JSX.Element | null {
      return genericResultBody(call, view, plainImageTabName(call))
    },
    resultMeta: genericResultMeta,
  }
}
