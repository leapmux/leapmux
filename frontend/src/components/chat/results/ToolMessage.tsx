import type { JSX } from 'solid-js'
import type { ToolCallRow } from '../model/row'
import type { ToolCall } from '../model/toolCall'
import type { ToolKind } from '../model/toolKind'
import type { ToolResultRenderContext } from '../renderContext'
import type { ToolCallDispatch } from './tools/index'
import CircleExclamationPoint from 'lucide-solid/icons/circle-exclamation-point'
import TriangleExclamationPoint from 'lucide-solid/icons/triangle-exclamation-point'
import { createEffect, createMemo, createSignal, Match, onCleanup, Show, Switch } from 'solid-js'
import { useCopyButton } from '~/hooks/useCopyButton'
import { rowDrawsRequest, rowDrawsResult, rowHasRequestRow } from '../model/derivations'
import { isToolFailureResult, isUnparsedToolResult } from '../model/toolCall'
import { toolCallStatusOutcome } from '../model/toolCallStatus'
import { isDisplayHeld } from '../renderContext'
import { toolOutcomeLabel } from '../results/toolOutcomeLabel'
import { ToolMessageLayout } from '../widgets/ToolMessageLayout'
import { McpContentList } from './genericToolCall'
import { ImageResultList } from './imageResult'
import { extraImages, resultImages } from './rowImages'
import { toolHintIcon } from './toolIconHints'
import { ToolMetadata } from './ToolMetadata'
import { ToolOutputFilePaths } from './ToolOutputFilePaths'
import { createToolRowState } from './toolRowState'
import { toolCallDisplayName } from './tools/header'
import { dispatchParts } from './tools/index'
import { toolCallMeta } from './tools/meta'
import { PlainTextResult } from './tools/plainTextResult'
import { ToolOutcomeHeader } from './ToolStatusHeader'
import { ToolTruncationNotice } from './ToolTruncationNotice'

/**
 * Draw one tool row from its call: one kind, one request, one result slot.
 *
 * A request row draws its own call alone, and never a result: it shows, and reports its
 * height, before the result exists. Its HEADER is the one exception. `header` is the
 * newest call of the span that the browser holds -- the result row's call once the
 * result landed -- so the header states the facts that only the result states, such as
 * a file path that a call learned while it ran. The header is one line while the row
 * is collapsed (see toolUseTitle), so the newer call changes its words and never the
 * height of the row.
 */
export function ToolMessage(props: { row: ToolCallRow, header?: ToolCall | undefined, context?: ToolResultRenderContext }): JSX.Element {
  const call = createMemo(() => props.row.call)
  const drawsRequest = () => rowDrawsRequest(props.row)
  const drawsResult = () => rowDrawsResult(props.row)
  const [summaryOverflows, setSummaryOverflows] = createSignal(false)
  const [titleOverflows, setTitleOverflows] = createSignal(false)
  const { parts, expanded, setExpanded, bodyContext, view } = createToolRowState({
    row: () => props.row,
    context: () => props.context,
    drawsResult,
    onSummaryOverflow: setSummaryOverflows,
  })
  // The call the header states. A result row heads itself with its own call, which is
  // already the newest. A request row heads itself with the newest call of its span.
  const headerParts = createMemo((): ToolCallDispatch<ToolKind> => {
    const header = props.header
    return header === undefined || drawsResult()
      ? parts()
      : dispatchParts(header)
  })
  const plain = createMemo(() => {
    const result = call().result
    return isToolFailureResult(result) || isUnparsedToolResult(result)
      ? result
      : undefined
  })
  const meta = createMemo(() => toolCallMeta(props.row))
  // Expand opens whatever the row clips: a body, a summary, or a header title.
  const previewClips = () => drawsRequest() && !drawsResult()
    && (props.context?.hasPendingPreview === true || props.context?.loneCall === true)
    && parts().renderer.pendingCollapsible?.(parts().parsed) === true
  const expandable = () => meta().collapsible || summaryOverflows() || titleOverflows()
    || props.context?.pendingPreviewExpandable === true || previewClips()
  // The outer toolbar and the context menu offer Expand exactly when this row does: only
  // the drawn row knows what its header clips.
  createEffect(() => props.context?.onExpandableChange?.(expandable()))
  // A row can stop drawing this tool row in place, as when an outcome note replaces its
  // body. Nothing then reads the expand state, so the report goes with the tool row.
  onCleanup(() => props.context?.onExpandableChange?.(false))
  const { copied, copy } = useCopyButton(() => meta().copyableContent() ?? undefined)
  // Quote and Copy use the same getter for this row's text.
  // context.onReply adds the blockquote syntax, so this handler supplies bare text.
  // The outer toolbar uses this getter also.
  // See quotableText in ~/components/chat/MessageBubble.tsx.
  const quote = (): (() => void) | undefined => {
    const reply = props.context?.onReply
    if (!reply || !meta().hasCopyable)
      return undefined
    // Read the current text when the user selects Quote.
    // A captured string would keep the output from the button's previous render.
    return () => {
      const text = meta().copyableContent()
      if (text !== null)
        reply(text)
    }
  }
  const statusOutcome = () => toolCallStatusOutcome(call().status)
  // Use the call's title for image tabs when it supplies one.
  // Otherwise, use the display name when the renderer puts that name first.
  // An absent title lets the bubble use its span-type fallback.
  const imageTitle = () => call().title ?? (parts().renderer.nameLeads
    ? toolCallDisplayName(call())
    : undefined)
  // Both image lists use these optional settings.
  // Omit an absent setting instead of passing an explicit undefined value.
  const imageOptions = () => {
    const title = imageTitle()
    const actions = props.context?.images
    return {
      ...(title !== undefined
        ? { title }
        : {}),
      ...(actions !== undefined
        ? { actions }
        : {}),
    }
  }
  // Offer Copy only when this row contains copyable text.
  // Offer Reply only when the host supplies a reply handler.
  const headerActions = () => {
    const reply = quote()
    return {
      contentCopied: copied(),
      copyContentLabel: meta().copyLabel ?? 'Copy',
      ...(meta().hasCopyable
        ? { onCopyContent: copy }
        : {}),
      ...(reply !== undefined
        ? { onReply: reply }
        : {}),
    }
  }
  // Read the dispatched call without creating another copy.
  const typedTitle = (): string => parts().renderer.outcomeTitle?.(parts().parsed) ?? toolOutcomeLabel(statusOutcome() ?? 'failed')
  // A typed result body can state its own outcome.
  // An unparsed result draws plain text and requires a separate outcome header.
  const bodyStatesOutcome = () => {
    const { renderer, resolved } = parts()
    return resolved !== undefined && (renderer.statesOwnOutcome?.(resolved) ?? false)
  }
  // Omit the summary prop when the renderer returns null.
  // hasContent treats that omission the same as an explicit null.
  const summaryProps = () => {
    const drawn = parts().renderer.summary?.(parts().parsed, view())
    return drawn === null
      ? {}
      : { summary: drawn }
  }
  return (
    <ToolMessageLayout
      role={props.row.role === 'result'
        ? 'result'
        : 'request'}
      hasRequest={props.row.role === 'result' && rowHasRequestRow(props.row)}
      icon={toolHintIcon(headerParts().call.icon) ?? headerParts().renderer.icon}
      toolName={toolCallDisplayName(headerParts().call)}
      title={headerParts().renderer.title(headerParts().parsed, props.context)}
      onTitleOverflow={setTitleOverflows}
      {...summaryProps()}
      {...(props.context !== undefined
        ? { context: props.context }
        : {})}
      expanded={expanded()}
      {...(expandable()
        ? {
            onToggleExpand: () => setExpanded(value => !value),
          }
        : {})}
      expandLabel={meta().expandLabel ?? 'Expand output'}
      headerActions={headerActions()}
      // The message host can draw these actions in the outer toolbar.
      // Hide this toolbar in that case to avoid duplicate controls and test IDs.
      showHeaderActions={!props.context?.hasOuterToolbar}
      alwaysVisible
    >
      {/* A result row beside its request row draws no header, and its summary rides on the
          header. A result row that restates its request draws the summary in its body, under
          the request row's header (see ToolSpanRowPosition.restatesRequest). */}
      <Show when={props.row.role === 'result' && props.row.restatesRequest === true && rowHasRequestRow(props.row)}>{summaryProps().summary}</Show>
      <Show when={drawsRequest() && call().metadata}>{items => <ToolMetadata items={items()} workingDir={props.context?.workingDir} homeDir={props.context?.homeDir} />}</Show>
      <Show when={drawsRequest()}>{parts().renderer.request?.(parts().parsed, view())}</Show>
      {/* A row of no span is its whole call: no pending row follows it and no result row
          replaces it, so it draws the preview of its call itself. */}
      <Show when={props.context?.loneCall && drawsRequest() && !drawsResult()}>{parts().renderer.pending?.(parts().parsed, view())}</Show>
      {/* Show all result content under one rowDrawsResult condition: only a result row
          draws it. A request row never reads its result, and the pending row below it
          draws the live output of a call that still runs.
          Separate conditions previously duplicated extra content and truncation notices.
          They also supplied image indices for images that the request row did not own.
          An image tab could then show the wrong image after reload.
          This shared condition keeps rendering and imagesForRow consistent. */}
      <Show when={drawsResult()}>
        <Show when={call().resultMetadata}>{items => <ToolMetadata items={items()} workingDir={props.context?.workingDir} homeDir={props.context?.homeDir} />}</Show>
        <ToolOutputFilePaths paths={call().outputFilePaths} />
        <Show when={parts().resolved === undefined}>{parts().renderer.withoutTypedResult?.(parts().parsed, view(), call().result !== undefined)}</Show>
        <Switch>
          <Match when={plain()}>{p => <PlainTextResult text={p().text} view={view()} />}</Match>
          {/* Match narrows the resolved call that dispatch supplies.
              The correlated renderer and call require no assertion here. */}
          <Match when={parts().resolved}>{c => parts().renderer.result(c(), view())}</Match>
        </Switch>
        <ToolTruncationNotice when={call().truncated} />
        {/* Extra content follows the result body, including its image indices.
            An offset of zero gives two images the same index.
            An image tab can then show the wrong image. */}
        <Show when={call().extraContent?.length}>
          <McpContentList
            items={call().extraContent!}
            indexOffset={resultImages(call()).length}
            title={toolCallDisplayName(call())}
            {...(props.context?.images !== undefined
              ? { actions: props.context?.images }
              : {})}
            holdDisplay={() => isDisplayHeld(props.context)}
            context={bodyContext()}
          />
        </Show>
        <Show when={call().images.length > 0}>
          <ImageResultList sources={call().images} indexOffset={extraImages(call()).length + resultImages(call()).length} {...imageOptions()} holdDisplay={() => isDisplayHeld(props.context)} readiness={props.context?.readiness} />
        </Show>
        <ToolOutcomeHeader
          when={statusOutcome() !== null && !bodyStatesOutcome()}
          icon={call().status === 'incomplete'
            ? TriangleExclamationPoint
            : CircleExclamationPoint}
          title={typedTitle()}
          {...(props.context !== undefined
            ? { context: props.context }
            : {})}
        />
      </Show>
    </ToolMessageLayout>
  )
}
