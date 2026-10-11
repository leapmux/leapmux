import type { JSX } from 'solid-js'
import type { MessageUiKey } from '../messageUiKeys'
import type { ToolCallRow } from '../model/row'
import type { ToolKind } from '../model/toolKind'
import type { MessageUiState, ToolProgressSource, ToolResultRenderContext } from '../renderContext'
import type { ToolCallDispatch } from './tools/index'
import type { ToolRowView } from './tools/renderer'
import CircleAlert from 'lucide-solid/icons/circle-alert'
import TriangleAlert from 'lucide-solid/icons/triangle-alert'
import { createMemo, createSignal, Match, Show, Switch, untrack } from 'solid-js'
import { useCopyButton } from '~/hooks/useCopyButton'
import { stripLeadingBlankLines } from '~/lib/normalizeProgressOutput'
import { useSharedExpandedState } from '../messageRenderers'
import { MESSAGE_UI_KEY } from '../messageUiKeys'
import { rowDrawsRequest, rowDrawsResult, rowHasRequestRow, toolRowPosition } from '../model/derivations'
import { isToolFailureResult, isUnparsedToolResult } from '../model/toolCall'
import { isFinishedToolCallStatus, toolCallStatusOutcome } from '../model/toolCallStatus'
import { toolOutcomeLabel } from '../results/toolOutcomeLabel'
import { toolInputSummary } from '../toolStyles.css'
import { TRUNCATION_NOTICE } from '../truncationNotice'
import { ToolMessageLayout } from '../widgets/ToolMessageLayout'
import { McpContentList } from './genericToolCall'
import { ImageResultList } from './imageResult'
import { extraImages, resultImages } from './rowImages'
import { toolHintIcon } from './toolIconHints'
import { ToolMetadata } from './ToolMetadata'
import { ToolOutputFilePaths } from './ToolOutputFilePaths'
import { toolCallDisplayName } from './tools/header'
import { dispatchParts } from './tools/index'
import { toolCallMeta } from './tools/meta'
import { PlainTextResult } from './tools/plainTextResult'
import { ToolOutcomeHeader } from './ToolStatusHeader'

/** Draw one tool row from its merged call: one kind, one request, one result slot. */
export function ToolMessage(props: { row: ToolCallRow, context?: ToolResultRenderContext, progress?: ToolProgressSource }): JSX.Element {
  const call = createMemo(() => props.row.call)
  const drawsRequest = () => rowDrawsRequest(props.row)
  const drawsResult = () => rowDrawsResult(props.row)
  // Dispatch supplies the correlated renderer and call to each hook.
  // This component requires no assertion to pair them again.
  const parts = createMemo((): ToolCallDispatch<ToolKind> => dispatchParts(call()))
  const plain = createMemo(() => {
    const result = call().result
    return isToolFailureResult(result) || isUnparsedToolResult(result) ? result : undefined
  })
  // One key per mounted row, read once: AGENT_PROMPT for an agent request row, TOOL_RESULT_EXPANDED otherwise.
  const expandKey = untrack((): MessageUiKey => (!drawsResult() && parts().renderer.requestExpandUiKey) || MESSAGE_UI_KEY.TOOL_RESULT_EXPANDED)
  const [expanded, setExpanded] = useSharedExpandedState(() => props.context, expandKey)
  const [summaryOverflows, setSummaryOverflows] = createSignal(false)

  // Local state supports an isolated render without a message-store host.
  // The expansion reader and writer must use the same state.
  // Replacing only the reader leaves the writer attached to another signal.
  // The view then ignores each expansion change.
  // This MessageUiState supplies both operations for this row's expansion key.
  // It forwards every other key to the message host.
  const rowUiState: MessageUiState = {
    get: key => key === MESSAGE_UI_KEY.TOOL_RESULT_EXPANDED
      ? expanded()
      : props.context?.getMessageUiState?.(key),
    set: (key, value) => {
      if (key === MESSAGE_UI_KEY.TOOL_RESULT_EXPANDED)
        setExpanded(value)
      else
        props.context?.setMessageUiState?.(key, value)
    },
  }
  const bodyContext = createMemo<ToolResultRenderContext>(() => {
    const context: ToolResultRenderContext = Object.create(props.context ?? null)
    Object.defineProperties(context, {
      getMessageUiState: { value: rowUiState.get },
      setMessageUiState: { value: rowUiState.set },
    })
    return context
  })

  // Solid compiles `view={view()}` to a getter.
  // A plain function allocates a new view for each child property read.
  // The new identity prevents the children from reusing the previous value.
  // A memo preserves the view until its inputs change.
  const view = createMemo((): ToolRowView => ({
    // Preserve the complete row position and its role constraints.
    // A field-by-field reconstruction can admit an invalid sibling.
    ...toolRowPosition(props.row),
    context: bodyContext(),
    drawsResult: drawsResult(),
    expanded,
    setExpanded: value => setExpanded(value),
    // The result body draws first, so its images start at index zero.
    // imagesForRow uses the same order. An image tab uses that list's index.
    imageIndexOffset: 0,
    onSummaryOverflow: setSummaryOverflows,
  }))
  const meta = createMemo(() => toolCallMeta(props.row))
  const expandable = () => meta().collapsible || summaryOverflows()
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
  // Show live output only before the call returns a result.
  // The Worker broadcasts this output on the ephemeral channel.
  // It removes that output when the persisted result arrives.
  // The completed row then uses its own text without changing the layout.
  const liveTail = () => !isFinishedToolCallStatus(call().status) && call().result === undefined ? props.progress?.liveTail() : undefined
  const truncated = () => call().truncated || liveTail()?.outputTruncated === true
  const statusOutcome = () => toolCallStatusOutcome(call().status)
  // Use the call's title for image tabs when it supplies one.
  // Otherwise, use the display name when the renderer puts that name first.
  // An absent title lets the bubble use its span-type fallback.
  const imageTitle = () => call().title ?? (parts().renderer.nameLeads ? toolCallDisplayName(call()) : undefined)
  // Both image lists use these optional settings.
  // Omit an absent setting instead of passing an explicit undefined value.
  const imageOptions = () => {
    const title = imageTitle()
    const actions = props.context?.images
    return {
      ...(title !== undefined ? { title } : {}),
      ...(actions !== undefined ? { actions } : {}),
    }
  }
  // Offer Copy only when this row contains copyable text.
  // Offer Reply only when the host supplies a reply handler.
  const headerActions = () => {
    const reply = quote()
    return {
      contentCopied: copied(),
      copyContentLabel: meta().copyLabel ?? 'Copy',
      ...(meta().hasCopyable ? { onCopyContent: copy } : {}),
      ...(reply !== undefined ? { onReply: reply } : {}),
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
    return drawn === null ? {} : { summary: drawn }
  }
  return (
    <ToolMessageLayout
      role={props.row.role === 'result' ? 'result' : 'request'}
      hasRequest={props.row.role === 'result' && rowHasRequestRow(props.row)}
      icon={toolHintIcon(call().icon) ?? parts().renderer.icon}
      toolName={toolCallDisplayName(call())}
      title={parts().renderer.title(parts().parsed, props.context)}
      {...summaryProps()}
      {...(props.context !== undefined ? { context: props.context } : {})}
      expanded={expanded()}
      {...(expandable() ? { onToggleExpand: () => setExpanded(value => !value) } : {})}
      expandLabel={meta().expandLabel ?? 'Expand output'}
      headerActions={headerActions()}
      // The message host can draw these actions in the outer toolbar.
      // Hide this toolbar in that case to avoid duplicate controls and test IDs.
      showHeaderActions={!props.context?.hasOuterToolbar}
      alwaysVisible
    >
      <Show when={drawsRequest() && call().metadata}>{items => <ToolMetadata items={items()} />}</Show>
      <Show when={drawsRequest()}>{parts().renderer.request?.(parts().parsed, view())}</Show>
      {/* Show all result content under one rowDrawsResult condition.
          A paired request row must show none of this content.
          Separate conditions previously duplicated extra content and truncation notices.
          They also supplied image indices for images that the request row did not own.
          An image tab could then show the wrong image after reload.
          This shared condition keeps rendering and imagesForRow consistent. */}
      <Show when={drawsResult()}>
        <ToolOutputFilePaths paths={call().outputFilePaths} />
        <Switch>
          <Match when={plain()}>{p => <PlainTextResult text={p().text} view={view()} />}</Match>
          {/* Match narrows the resolved call that dispatch supplies.
              The correlated renderer and call require no assertion here. */}
          <Match when={parts().resolved}>{c => parts().renderer.result(c(), view())}</Match>
          <Match when={liveTail()?.outputTail}>{tail => <PlainTextResult text={stripLeadingBlankLines(tail())} view={view()} />}</Match>
        </Switch>
        <Show when={truncated()}><div class={toolInputSummary}>{TRUNCATION_NOTICE}</div></Show>
        {/* Extra content follows the result body, including its image indices.
            An offset of zero gives two images the same index.
            An image tab can then show the wrong image. */}
        <Show when={call().extraContent?.length}><McpContentList items={call().extraContent!} indexOffset={resultImages(call()).length} title={toolCallDisplayName(call())} {...(props.context?.images !== undefined ? { actions: props.context?.images } : {})} holdDisplay={() => props.context?.syntaxHighlightingPaused?.() === true || props.context?.textSelectionActive?.() === true} context={bodyContext()} /></Show>
        <Show when={call().images.length > 0}>
          <ImageResultList sources={call().images} indexOffset={extraImages(call()).length + resultImages(call()).length} {...imageOptions()} holdDisplay={() => props.context?.syntaxHighlightingPaused?.() === true || props.context?.textSelectionActive?.() === true} />
        </Show>
        <ToolOutcomeHeader when={statusOutcome() !== null && !bodyStatesOutcome()} icon={call().status === 'incomplete' ? TriangleAlert : CircleAlert} title={typedTitle()} {...(props.context !== undefined ? { context: props.context } : {})} />
      </Show>
    </ToolMessageLayout>
  )
}
