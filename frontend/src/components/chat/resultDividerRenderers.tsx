import type { JSXElement } from 'solid-js'
import type { TurnEnd } from './model/divider'
import { Show } from 'solid-js'
import { pluralize } from '~/lib/plural'
import { resultDivider, resultErrorDetail } from './messageStyles.css'

/**
 * What the turn cost, stated beside the rule.
 *
 * The worker measures both for every provider and nothing drew either before: the tool
 * count was parsed and dropped, and the cost reached the session totals alone. A turn
 * that used no tool states none rather than "0 tools", because a reader learns nothing
 * from a zero they did not ask about.
 *
 * The DURATION is deliberately absent. Every provider already writes it into its own
 * label -- "Turn ended (12s)" -- so a second copy here would say it twice.
 */
function dividerTotals(meta: TurnEnd['meta']): string {
  if (!meta)
    return ''
  const parts: string[] = []
  if (meta.numToolUses !== undefined && meta.numToolUses > 0)
    parts.push(pluralize(meta.numToolUses, 'tool'))
  // Four decimals, the same precision the agent info card states a session total in.
  if (meta.costUsd !== undefined && meta.costUsd > 0)
    parts.push(`$${meta.costUsd.toFixed(4)}`)
  return parts.join(' \u00B7 ')
}

/**
 * The single renderer for a `result_divider` (turn-end) message across providers.
 * Draws a {@link TurnEnd}: the label in danger color when `isError`, the turn totals
 * beside it, and optionally a `<pre>` detail block. The danger color is an inline
 * style (not a class) on purpose -- it preserves the exact markup the four
 * per-provider divider renderers emitted before they were unified onto this model.
 */
export function ResultDivider(props: { model: TurnEnd }): JSXElement {
  return (
    <>
      {/*
        The test id marks the TURN-END rule specifically. `resultDivider` is a
        hashed class and is shared with the compaction-boundary rule inside a
        centered bubble (NotificationDivider), which does NOT bleed -- so a class
        selector could not tell the two apart.
      */}
      <div
        class={resultDivider}
        data-testid="result-divider"
        style={props.model.isError ? { color: 'var(--danger)' } : undefined}
      >
        {props.model.label}
        <Show when={dividerTotals(props.model.meta)}>
          {totals => <span data-testid="result-divider-totals">{totals()}</span>}
        </Show>
      </div>
      {props.model.detail && <pre class={resultErrorDetail}>{props.model.detail}</pre>}
    </>
  )
}
