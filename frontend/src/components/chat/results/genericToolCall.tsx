import type { JSX } from 'solid-js'
import type { McpContentItem } from '../model/mcpToolCall'
import type { ToolCallStatus } from '../model/toolCallStatus'
import type { GenericToolRequest, GenericToolResult } from '../model/tools/generic'
import type { ImageRenderActions, ToolResultRenderContext } from '../renderContext'
import { createMemo, For, Match, Show, Switch } from 'solid-js'
import { prettifyJson } from '~/lib/jsonFormat'
import { getToolResultExpanded } from '../messageRenderers'
import { isFinishedToolCallStatus } from '../model/toolCallStatus'
import { toolInputSummary, toolMessage, toolResultError, toolResultPrompt } from '../toolStyles.css'
import { CollapsibleContent } from './CollapsibleContent'
import { EMPTY_RESULT_NOTICE } from './emptyResultNotice'
import { ImageResultView } from './imageResult'
import { textNeedsCollapse, useCollapsedLines } from './useCollapsedLines'

function contentText(item: McpContentItem): string {
  switch (item.type) {
    case 'text': return item.text
    case 'resource': return [item.uri, item.text].filter(value => value !== undefined).join('\n')
    case 'unknown': return prettifyJson(item.raw)
    case 'image': return item.source.description ?? ''
  }
}

/** The text that Copy writes for a list of generic content blocks. */
export function contentBlocksCopyable(content: readonly McpContentItem[]): string {
  return content.map(contentText).filter(Boolean).join('\n\n')
}

/**
 * Read the content blocks from a generic result.
 * A malformed result can carry another kind's shape without a content array.
 * That shape draws an empty result instead of crashing the renderer.
 */
function genericContent(result: GenericToolResult): readonly McpContentItem[] {
  return Array.isArray(result.content) ? result.content : []
}

/** The text that Copy writes for a generic result. */
export function genericResultCopyable(result: GenericToolResult): string {
  return [contentBlocksCopyable(genericContent(result)), result.structuredJson, result.error].filter(Boolean).join('\n\n')
}

/** Whether a generic request or result exceeds the collapsed display. */
export function genericResultCollapsible(result: GenericToolResult, argsJson: string): boolean {
  return [argsJson, result.structuredJson, result.error, ...genericContent(result).map(item => item.type === 'image' ? undefined : item.type === 'resource' ? item.text : contentText(item))]
    .some(text => text !== undefined && textNeedsCollapse(text))
}

function McpTextView(props: { outputPreview?: boolean, text: string, markdown?: boolean, expanded: () => boolean, context?: ToolResultRenderContext }): JSX.Element {
  const collapsed = useCollapsedLines({ text: () => props.text, expanded: () => props.expanded() })
  return <CollapsibleContent outputPreview={props.outputPreview === true} kind={props.markdown ? 'markdown-tool-result' : 'pre'} text={props.text} display={collapsed.display()} isCollapsed={collapsed.isCollapsed()} {...(props.context !== undefined ? { context: props.context } : {})} />
}

/** The list of content blocks a generic result holds, numbered from `indexOffset`. */
export function McpContentList(props: {
  items: readonly McpContentItem[]
  indexOffset?: number
  title?: string
  failed?: boolean
  actions?: ImageRenderActions
  holdDisplay?: () => boolean
  context?: ToolResultRenderContext
  expanded?: () => boolean
}): JSX.Element {
  const expanded = () => props.expanded?.() ?? getToolResultExpanded(props.context)
  // Count images independently from the text blocks between them.
  // imagesForRow uses this same order when an image tab resolves its index.
  const imageOrdinals = createMemo(() => {
    let seen = props.indexOffset ?? 0
    return props.items.map(item => item.type === 'image' ? seen++ : -1)
  })
  return (
    // `imageOrdinals` fills every position, so `?? -1` is the type-level guard alone.
    <For each={props.items}>
      {(item, index) => (
        <McpContentItemView
          item={item}
          imageIndex={imageOrdinals()[index()] ?? -1}
          {...(props.title !== undefined ? { title: props.title } : {})}
          {...(props.failed !== undefined ? { failed: props.failed } : {})}
          {...(props.actions !== undefined ? { actions: props.actions } : {})}
          {...(props.holdDisplay !== undefined ? { holdDisplay: props.holdDisplay } : {})}
          {...(props.context !== undefined ? { context: props.context } : {})}
          expanded={expanded}
        />
      )}
    </For>
  )
}

/**
 * Render a generic tool's arguments, returned blocks, structured data, and error.
 * The caller supplies the header.
 */
export function GenericToolBody(props: {
  request: GenericToolRequest
  result: GenericToolResult
  status: ToolCallStatus
  actions?: ImageRenderActions
  holdDisplay?: () => boolean
  context?: ToolResultRenderContext
  expanded?: () => boolean
  /** The number of result images that precede this content list. */
  indexOffset?: number
  /** The name an image tab takes, when the caller holds a better one than the row. */
  title?: string
}): JSX.Element {
  const expanded = () => props.expanded?.() ?? getToolResultExpanded(props.context)
  const argsText = () => props.request.argsText ?? (Object.keys(props.request.args).length > 0 ? prettifyJson(props.request.args) : '')
  const failed = () => props.status === 'failed'
  const structuredMetadata = () => props.result.structuredJsonRole === 'metadata' ? props.result.structuredJson : undefined
  const structuredOutput = () => props.result.structuredJsonRole !== 'metadata' ? props.result.structuredJson : undefined
  const structuredText = (text: string, outputPreview: boolean): JSX.Element => (
    <>
      <div class={toolInputSummary}>Structured</div>
      <McpTextView text={text} outputPreview={outputPreview} expanded={expanded} {...(props.context !== undefined ? { context: props.context } : {})} />
    </>
  )
  return (
    <div class={toolMessage}>
      <Show when={argsText()}>
        <div class={toolInputSummary}>Arguments</div>
        <McpTextView text={argsText()} expanded={expanded} {...(props.context !== undefined ? { context: props.context } : {})} />
      </Show>
      <Show when={structuredMetadata()}>{text => structuredText(text(), false)}</Show>
      <Show when={genericContent(props.result).length > 0}>
        <McpContentList items={genericContent(props.result)} {...(props.indexOffset !== undefined ? { indexOffset: props.indexOffset } : {})} {...(props.title !== undefined ? { title: props.title } : {})} failed={failed()} {...(props.actions !== undefined ? { actions: props.actions } : {})} {...(props.holdDisplay !== undefined ? { holdDisplay: props.holdDisplay } : {})} {...(props.context !== undefined ? { context: props.context } : {})} expanded={expanded} />
      </Show>
      <Show when={structuredOutput()}>{text => structuredText(text(), true)}</Show>
      <Show when={props.result.error}>
        <div class={toolResultError}><McpTextView outputPreview text={props.result.error!} expanded={expanded} {...(props.context !== undefined ? { context: props.context } : {})} /></div>
      </Show>
      <Show when={isFinishedToolCallStatus(props.status) && genericContent(props.result).length === 0 && !props.result.structuredJson && !props.result.error}>
        <div class={toolResultPrompt}>{EMPTY_RESULT_NOTICE}</div>
      </Show>
    </div>
  )
}

function McpContentItemView(props: { item: McpContentItem, imageIndex?: number, title?: string, failed?: boolean, actions?: ImageRenderActions, holdDisplay?: () => boolean, context?: ToolResultRenderContext, expanded: () => boolean }): JSX.Element {
  return (
    <Switch>
      <Match when={props.item.type === 'text' ? props.item : undefined}>
        {item => <McpTextView outputPreview text={item().text} markdown={!props.failed} expanded={props.expanded} {...(props.context !== undefined ? { context: props.context } : {})} />}
      </Match>
      <Match when={props.item.type === 'image' ? props.item : undefined}>
        {item => (
          <ImageResultView
            source={item().source}
            {...(props.imageIndex !== undefined ? { index: props.imageIndex } : {})}
            {...(props.title !== undefined ? { title: props.title } : {})}
            {...(props.actions !== undefined ? { actions: props.actions } : {})}
            {...(props.holdDisplay !== undefined ? { holdDisplay: props.holdDisplay } : {})}
          />
        )}
      </Match>
      <Match when={props.item.type === 'resource' ? props.item : undefined}>
        {item => <McpResourceView item={item()} expanded={props.expanded} {...(props.context !== undefined ? { context: props.context } : {})} />}
      </Match>
      <Match when={props.item.type === 'unknown' ? props.item : undefined}>
        {item => <McpTextView outputPreview text={prettifyJson(item().raw)} expanded={props.expanded} {...(props.context !== undefined ? { context: props.context } : {})} />}
      </Match>
    </Switch>
  )
}

function McpResourceView(props: {
  item: Extract<McpContentItem, { type: 'resource' }>
  expanded: () => boolean
  context?: ToolResultRenderContext
}): JSX.Element {
  return (
    <>
      <div class={toolInputSummary}>
        [resource:
        {' '}
        {props.item.uri}
        {props.item.mimeType ? ` (${props.item.mimeType})` : ''}
        ]
      </div>
      <Show when={props.item.text !== undefined}>
        <McpTextView outputPreview text={props.item.text!} expanded={props.expanded} {...(props.context !== undefined ? { context: props.context } : {})} />
      </Show>
    </>
  )
}
