import type { JSX } from 'solid-js'
import type { RenderContext } from '../../../messageRenderers'
import type { ToolMessageSource } from '../../../results/toolPresentation'
import type { ZCodeRow } from '../extractors/toolCommon'
import type { ParsedMessageContent } from '~/lib/messageParser'
import { createMemo, Show } from 'solid-js'
import { ZCODE_TOOL, ZCODE_TOOL_KIND } from '~/generated/contracts/zcode-protocol'
import { pickString } from '~/lib/jsonPick'
import { ToolMessage } from '../../../results/ToolMessage'
import { MarkdownPlanLayout } from '../../../widgets/MarkdownPlanLayout'
import { zcodeExtractTool, zcodeRow, zcodeToolInput } from '../extractors/toolCommon'
import { zcodeToolMessageSource } from '../toolPresentation'

interface ToolProps {
  parsed: unknown
  context?: RenderContext
}

/** Build the row of one side of a span, with the sources that side needs to resolve. */
function zcodeSideRow(parent: unknown, parsed: ParsedMessageContent | undefined, context: RenderContext | undefined, request: ParsedMessageContent | undefined): ZCodeRow {
  return {
    ...zcodeRow(parent, context?.spanType, request, parsed?.supplementalContent),
    result: context?.sources?.result(),
  }
}

/**
 * Resolve the native ZCode events before the shared component renders the tool.
 *
 * Both sides of a span draw from the same model, so an opener and its result agree on
 * the icon, the title and the label without a second table to keep in step.
 */
function ZCodeToolMessage(props: ToolProps): JSX.Element {
  // A scheduled row IS the request of its own span, and the store resolves no
  // separate one for it.
  const request = () => props.context?.sources?.request()
    ?? (zcodeExtractTool(props.parsed)?.kind === ZCODE_TOOL_KIND.Scheduled ? props.context?.sources?.current() : undefined)
  const sideSource = (parsed: ParsedMessageContent | undefined): ToolMessageSource | undefined => parsed
    ? zcodeToolMessageSource(zcodeSideRow(parsed.parentObject, parsed, props.context, request()), parsed) ?? undefined
    : undefined
  const source = createMemo(() => {
    const current = props.context?.sources?.current()
    return zcodeToolMessageSource(zcodeSideRow(props.parsed, current, props.context, request()), current) ?? undefined
  })
  const requestSource = createMemo(() => sideSource(props.context?.sources?.request()))
  const resultSource = createMemo(() => sideSource(props.context?.sources?.result()))
  return (
    <Show when={source()}>
      {resolved => <ToolMessage source={resolved()} request={requestSource()} result={resultSource()} context={props.context} />}
    </Show>
  )
}

/**
 * A ZCode tool_use row.
 *
 * A plan leaves the shared tool path, because it is not a tool body: every provider
 * draws a proposed plan through `MarkdownPlanLayout`.
 */
export function ZCodeToolExecutionRenderer(props: ToolProps): JSX.Element {
  const plan = createMemo(() => {
    const row = zcodeRow(props.parsed, props.context?.spanType, props.context?.sources?.request())
    return row.toolName === ZCODE_TOOL.ExitPlanMode ? pickString(zcodeToolInput(row), 'plan') : ''
  })
  return (
    <Show when={!plan()} fallback={<MarkdownPlanLayout toolName={ZCODE_TOOL.ExitPlanMode} title="Proposed Plan" planText={plan()} context={props.context} />}>
      <ZCodeToolMessage parsed={props.parsed} context={props.context} />
    </Show>
  )
}

/**
 * A ZCode tool_result row.
 *
 * `resultAbsent` says that the agent sent NO result and that LeapMux states so in a
 * note of its own. The row then draws nothing: an empty body reads as "the tool
 * returned nothing", which asserts something the agent never reported.
 */
export function ZCodeToolResultRenderer(props: ToolProps): JSX.Element {
  return (
    <Show when={!props.context?.resultAbsent}>
      <ZCodeToolMessage parsed={props.parsed} context={props.context} />
    </Show>
  )
}
