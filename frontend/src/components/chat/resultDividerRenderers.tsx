import type { JSXElement } from 'solid-js'
import type { ResultDividerModel } from './providers/registry'
import type { AgentProvider } from '~/generated/leapmux/v1/agent_pb'
import { resultDivider, resultErrorDetail } from './messageStyles.css'
import { pluginFor } from './providers/registry'

/**
 * The single renderer for a `result_divider` (turn-end) message across providers.
 * Draws a {@link ResultDividerModel}: the label in danger color when `isError`,
 * optionally followed by a `<pre>` detail block. The danger color is an inline
 * style (not a class) on purpose -- it preserves the exact markup the four
 * per-provider divider renderers emitted before they were unified onto this model.
 */
function ResultDivider(props: { model: ResultDividerModel }): JSXElement {
  return (
    <>
      <div class={resultDivider} style={props.model.isError ? { color: 'var(--danger)' } : undefined}>
        {props.model.label}
      </div>
      {props.model.detail && <pre class={resultErrorDetail}>{props.model.detail}</pre>}
    </>
  )
}

/**
 * Render a `result_divider` via the provider's `resultDivider` hook -- the sole
 * render path for the category. MessageBubble special-cases the category and
 * falls back to the raw-JSON renderer when this returns null (an unrecognized
 * turn-end shape). Dispatches strictly by the message's own provider: a message
 * only reaches here after classifyMessage produced `result_divider`, which it
 * does only for a registered provider, so there is no Claude fallback.
 */
export function renderResultDivider(parsed: unknown, agentProvider?: AgentProvider): JSXElement | null {
  const plugin = pluginFor(agentProvider)
  const model = plugin?.resultDivider?.(parsed)
  return model ? <ResultDivider model={model} /> : null
}
