import type { JSX } from 'solid-js'
import type { ToolCallRow } from '../ir/row'
import type { FailedResult, UnparsedResult } from '../ir/toolCall'
import type { ToolKind } from '../ir/toolKind'
import type { RenderContext } from '../messageRenderers'
import type { MessageUiKey } from '../messageUiKeys'
import type { MessageUiState, ToolProgressSource } from '../renderContext'
import type { ToolCallDispatch } from './tools/index'
import type { ToolRowView } from './tools/renderer'
import CircleAlert from 'lucide-solid/icons/circle-alert'
import { createMemo, createSignal, Match, Show, Switch, untrack } from 'solid-js'
import { useCopyButton } from '~/hooks/useCopyButton'
import { stripLeadingBlankLines } from '~/lib/normalizeProgressOutput'
import { extraImages, resultImages, rowDrawsResult, rowHasRequestRow, toolRowPosition } from '../ir/derivations'
import { isFailedResult, isUnparsedResult } from '../ir/toolCall'
import { toolOutcomeLabel } from '../ir/toolOutcomeLabel'
import { isFinishedToolStatus, toolRowStatusOutcome } from '../ir/toolRowStatus'
import { useSharedExpandedState } from '../messageRenderers'
import { MESSAGE_UI_KEY } from '../messageUiKeys'
import { toolInputSummary } from '../toolStyles.css'
import { TRUNCATION_NOTICE } from '../truncationNotice'
import { ToolMessageLayout } from '../widgets/ToolMessageLayout'
import { McpContentList } from './genericToolCall'
import { ImageResultList } from './imageResult'
import { toolHintIcon } from './toolIconHints'
import { ToolMetadata } from './ToolMetadata'
import { toolCallDisplayName } from './tools/header'
import { dispatchParts } from './tools/index'
import { toolCallMeta } from './tools/meta'
import { PlainTextResult } from './tools/plainTextResult'
import { ToolOutcomeHeader } from './ToolStatusHeader'

/** Draw one tool row from its merged call: one kind, one request, one result slot. */
export function ToolMessage(props: { row: ToolCallRow, context?: RenderContext, progress?: ToolProgressSource }): JSX.Element {
  const call = createMemo(() => props.row.call)
  const drawsResult = () => rowDrawsResult(props.row)
  // The renderer, the call and the views of it the hooks read, dispatched as ONE
  // correlated pair. No assertion re-pairs them here; the dispatch owns the pairing.
  const parts = createMemo((): ToolCallDispatch<ToolKind> => dispatchParts(call()))
  const plain = createMemo(() => (isFailedResult(call().result) || isUnparsedResult(call().result)) ? call().result as FailedResult | UnparsedResult : undefined)
  // One key per mounted row, read once: AGENT_PROMPT for an agent request row, TOOL_RESULT_EXPANDED otherwise.
  const expandKey = untrack((): MessageUiKey => (!drawsResult() && parts().renderer.requestExpandUiKey) || MESSAGE_UI_KEY.TOOL_RESULT_EXPANDED)
  const [expanded, setExpanded] = useSharedExpandedState(() => props.context, expandKey)
  const [summaryOverflows, setSummaryOverflows] = createSignal(false)

  // The local state supports isolated renders without a message-store host.
  //
  // The READ and the WRITE of the expanded flag move together. An override that
  // took the read alone left the inherited writer in place, so a body renderer
  // that toggled the flag wrote its own local signal while this override kept
  // answering every read -- the write then had no effect that anybody could see.
  //
  // The pair is stated ONCE as a {@link MessageUiState} -- this row's own expand
  // toggle answers its key, every other key forwards to the message host -- and
  // the context overlay installs its two members, so the capability and the
  // context cannot disagree about what a body's toggle writes.
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
  const bodyContext = createMemo<RenderContext>(() => {
    const context: RenderContext = Object.create(props.context ?? null)
    Object.defineProperties(context, {
      getMessageUiState: { value: rowUiState.get },
      setMessageUiState: { value: rowUiState.set },
    })
    return context
  })

  // A MEMO, not a plain function. `view={view()}` compiles to a getter, so a plain
  // function re-allocated the whole view -- including `toolRowPosition` and an
  // `extraImages` flatMap -- on every `props.view.<field>` read in every child, and
  // the children saw a new object identity each time and could not short-circuit.
  const view = createMemo((): ToolRowView => ({
    // The row's own position, carried WHOLE. Rebuilding it field by field is what let
    // a view claim a sibling its role rules out.
    ...toolRowPosition(props.row),
    context: bodyContext(),
    drawsResult: drawsResult(),
    expanded,
    setExpanded: value => setExpanded(value),
    // The RESULT body draws first, so its pictures are numbered from zero.
    // `imagesForIR` states the same order, and an image tab addresses a picture by
    // its index in that list.
    imageIndexOffset: 0,
    onSummaryOverflow: setSummaryOverflows,
  }))
  const meta = createMemo(() => toolCallMeta(props.row))
  const expandable = () => meta().collapsible || summaryOverflows()
  const { copied, copy } = useCopyButton(() => meta().copyableContent() ?? undefined)
  // Quote writes the row's own text into the composer -- the SAME text Copy writes,
  // read from the same getter, so the two can never state different words for one
  // row. `context.onReply` already wraps the text as a blockquote, so this hands it
  // the bare text. The bubble's outer toolbar derives its Quote from this same
  // getter; see `quotableText` in ~/components/chat/MessageBubble.tsx.
  const quote = (): (() => void) | undefined => {
    const reply = props.context?.onReply
    if (!reply || !meta().hasCopyable)
      return undefined
    // The text is read INSIDE the handler, not captured beside it: a captured
    // string is the value of one reactive pass, and a row that streams would quote
    // the output it held when the button was last built.
    return () => {
      const text = meta().copyableContent()
      if (text !== null)
        reply(text)
    }
  }
  // The live output of a call that has NOT returned. The worker broadcasts it on
  // the ephemeral channel and drops it when the result row lands, so the finished
  // row keeps drawing its own persisted text and the row never re-lays out.
  const liveTail = () => !isFinishedToolStatus(call().status) && call().result === undefined ? props.progress?.liveTail() : undefined
  const truncated = () => call().truncated || liveTail()?.outputTruncated === true
  const statusOutcome = () => toolRowStatusOutcome(call().status)
  // The image tabs' name: the call's own title when it stated one, else the
  // leading-name renderers' display name, else the bubble's span-type fallback.
  const imageTitle = () => call().title ?? (parts().renderer.nameLeads ? toolCallDisplayName(call()) : undefined)
  // Both lists of images take the same optional dressings; each narrows once here
  // so an absent one stays ABSENT rather than an explicitly undefined key.
  const imageDressings = () => {
    const title = imageTitle()
    const actions = props.context?.images
    return {
      ...(title !== undefined ? { title } : {}),
      ...(actions !== undefined ? { actions } : {}),
    }
  }
  // The row's own toolbar actions, narrowed the same way: Copy only when the row
  // holds copyable text, Reply only when the host handed a reply channel.
  const headerActions = () => {
    const reply = quote()
    return {
      contentCopied: copied(),
      copyContentLabel: meta().copyLabel ?? 'Copy',
      ...(meta().hasCopyable ? { onCopyContent: copy } : {}),
      ...(reply !== undefined ? { onReply: reply } : {}),
    }
  }
  // Reads the dispatched parts rather than spreading the call a second time.
  const typedTitle = (): string => parts().renderer.outcomeTitle?.(parts().parsed) ?? toolOutcomeLabel(statusOutcome() ?? 'failed')
  // Whether the body this row actually DREW states the outcome itself. A result the
  // kind could not parse draws plain text, which states none -- so the answer is no
  // whenever the typed body never ran.
  const bodyStatesOutcome = () => {
    const { renderer, resolved } = parts()
    return resolved !== undefined && (renderer.statesOwnOutcome?.(resolved) ?? false)
  }
  // The summary the kind's own hook drew, when it drew one: a null answer stays an
  // ABSENT prop rather than an explicitly null one, which `hasContent` reads the same.
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
      // The message host draws these same actions in the bubble's own toolbar
      // whenever the row feeds `toolCallMeta`, so drawing them here too put
      // two toolbars with one test id on the row.
      showHeaderActions={!props.context?.hasOuterToolbar}
      alwaysVisible
    >
      <Show when={call().metadata}>{items => <ToolMetadata items={items()} />}</Show>
      {parts().renderer.request?.(parts().parsed, view())}
      {/* The RESULT SIDE, gated ONCE.
          A span whose two rows are both drawn puts every one of these on the result
          row alone -- `rowDrawsResult` is that rule, and `imagesForIR` states the same
          order over the same rule. The gate used to be spelled at each piece
          separately, and two of them were missed: a paired REQUEST row drew the
          call's extra content and the truncation notice a second time, and it handed
          `onOpenImage` an index for a picture `imagesForIR` reports no row as holding
          -- so the tab that index opens after a reload shows a different picture. One
          `Show` makes the next piece added here correct by construction. */}
      <Show when={drawsResult()}>
        <Switch>
          <Match when={plain()}>{p => <PlainTextResult text={p().text} view={view()} />}</Match>
          {/* The typed body takes the RESOLVED call the dispatch holds, narrowed by the
              Match itself -- the correlated pair needs no assertion to reach the hook. */}
          <Match when={parts().resolved}>{c => parts().renderer.result(c(), view())}</Match>
          <Match when={liveTail()?.outputTail}>{tail => <PlainTextResult text={stripLeadingBlankLines(tail())} view={view()} />}</Match>
        </Switch>
        <Show when={truncated()}><div class={toolInputSummary}>{TRUNCATION_NOTICE}</div></Show>
        {/* The extra content draws UNDER the result body, so its pictures are numbered
            after the result's. An offset of 0 here gave two pictures the same index, and
            a tab opened from one of them showed the other. */}
        <Show when={call().extraContent?.length}><McpContentList items={call().extraContent!} indexOffset={resultImages(call()).length} title={toolCallDisplayName(call())} {...(props.context?.images !== undefined ? { actions: props.context?.images } : {})} holdDisplay={() => props.context?.syntaxHighlightingPaused?.() === true || props.context?.textSelectionActive?.() === true} context={bodyContext()} /></Show>
        <Show when={call().images.length > 0}>
          <ImageResultList sources={call().images} indexOffset={extraImages(call()).length + resultImages(call()).length} {...imageDressings()} holdDisplay={() => props.context?.syntaxHighlightingPaused?.() === true || props.context?.textSelectionActive?.() === true} />
        </Show>
        <ToolOutcomeHeader when={statusOutcome() !== null && !bodyStatesOutcome()} icon={CircleAlert} title={typedTitle()} {...(props.context !== undefined ? { context: props.context } : {})} />
      </Show>
    </ToolMessageLayout>
  )
}
