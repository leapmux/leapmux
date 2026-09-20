import type { JSX } from 'solid-js'
import type { RenderContext } from '../../messageRenderers'
import { render } from '@solidjs/testing-library'
import { AgentProvider } from '~/generated/proto/leapmux/v1/agent_pb'
import { isObject } from '~/lib/jsonPick'
import { renderMessageContent } from '../../messageContentRenderer'
import { providerFor } from '../registry'
import { input } from '../testUtils'
// Side-effect: the helper dispatches through the registry, so the plugin must be in it.
import './plugin'

/**
 * One ZCode row, drawn through the dispatcher the way a mounted row is.
 *
 * Through `renderMessageContent`, not a provider component: the dispatcher is what
 * resolves the extraction and wraps it in the shared completion chrome, so a test that
 * reached past it could pass while the mounted row drew something else.
 *
 * A COMPONENT rather than a bare call, because a bare call inside `render(fn)` freezes
 * the row at its first payload: Solid calls `fn` once and inserts the result, while a
 * component re-reads its props.
 */
export function ZCodeRowView(props: { parsed: unknown, context?: RenderContext }): JSX.Element {
  const category = () => {
    const parsed = isObject(props.parsed) ? props.parsed : undefined
    return providerFor(AgentProvider.ZCODE)!.transcript.classify({
      ...input(parsed),
      agentProvider: AgentProvider.ZCODE,
      ...(props.context?.spanType !== undefined ? { spanType: props.context.spanType } : {}),
    })
  }
  return <>{renderMessageContent(props.parsed, props.context, category(), AgentProvider.ZCODE)}</>
}

/** Render one ZCode row and return the testing-library handle. */
export function renderZCodeRow(parsed: unknown, context?: RenderContext): ReturnType<typeof render> {
  return render(() => <ZCodeRowView parsed={parsed} {...(context !== undefined ? { context } : {})} />)
}
