import type { Component } from 'solid-js'
import type { AgentProvider } from '~/generated/proto/leapmux/v1/agent_pb'
import type { ContextUsageInfo } from '~/stores/agentSession.store'
import Info from 'lucide-solid/icons/info'
import { createMemo, Show } from 'solid-js'
import { pluginFor } from '~/components/chat/providers/registry'
import { PIP_GRID_COLUMNS, PIP_GRID_PIPS, PipGrid } from '~/components/common/PipGrid'
import { Tooltip } from '~/components/common/Tooltip'

interface ContextUsageGridProps {
  contextUsage?: ContextUsageInfo
  modelContextWindow?: number
  agentProvider?: AgentProvider
  size: number
}

export const DEFAULT_CONTEXT_WINDOW = 200_000

// The autocompact buffer (percentage of the context window a provider reserves)
// is declared per-provider in its plugin; default 0 for providers with none.
export function contextBufferPct(agentProvider?: AgentProvider): number {
  return pluginFor(agentProvider)?.contextBufferPct ?? 0
}

/** Resolve the effective context window from usage data, model metadata, or the default. */
export function resolveContextWindow(usage: ContextUsageInfo, modelContextWindow?: number): number {
  if (usage.contextWindow && usage.contextWindow > 0)
    return usage.contextWindow
  if (modelContextWindow && modelContextWindow > 0)
    return modelContextWindow
  return DEFAULT_CONTEXT_WINDOW
}

/** Total context size. Prefer an authoritative provider-reported total when present. */
export function contextSize(usage: ContextUsageInfo): number {
  if (usage.contextTokens != null)
    return usage.contextTokens
  return usage.cacheCreationInputTokens + usage.cacheReadInputTokens + usage.inputTokens + (usage.outputTokens ?? 0)
}

/**
 * Compute context usage percentage from structured token data.
 * Accounts for the autocompact buffer: usable capacity = contextWindow * (1 - buffer%).
 * Uses the context window from usage data, then modelContextWindow, then DEFAULT_CONTEXT_WINDOW.
 */
export function computePercentage(usage: ContextUsageInfo | undefined, modelContextWindow?: number, agentProvider?: AgentProvider): number | null {
  if (!usage)
    return null
  const total = contextSize(usage)
  if (total <= 0)
    return null
  const contextWindow = resolveContextWindow(usage, modelContextWindow)
  const usable = contextWindow * (1 - contextBufferPct(agentProvider) / 100)
  if (usable <= 0)
    return null
  return Math.min(100, (total / usable) * 100)
}

/** Map a percentage (0-100) to the number of filled pips (0-9). */
function filledCount(pct: number): number {
  if (pct <= 0)
    return 0
  if (pct >= 81)
    return PIP_GRID_PIPS
  return Math.ceil(pct / 10)
}

// Fill order: bottom-left to top-right (row 2 L-R, row 1 L-R, row 0 L-R), so
// the meter fills upwards the way a bar chart does. `PipGrid` takes its fills
// row-major, so `fills` below maps this order onto that one.
const fillOrder: [row: number, col: number][] = [
  [2, 0],
  [2, 1],
  [2, 2],
  [1, 0],
  [1, 1],
  [1, 2],
  [0, 0],
  [0, 1],
  [0, 2],
]

export const ContextUsageGrid: Component<ContextUsageGridProps> = (props) => {
  const percentage = createMemo(() => computePercentage(props.contextUsage, props.modelContextWindow, props.agentProvider))

  const filled = createMemo(() => {
    const pct = percentage()
    return pct != null ? filledCount(pct) : 0
  })

  const warning = createMemo(() => (percentage() ?? 0) >= 91)

  const activeColor = () => warning() ? 'var(--context-grid-warning)' : 'currentColor'

  const fills = createMemo(() => {
    // eslint-disable-next-line solid/reactivity -- `active` is read by the Array.from mapper below, in this same memo evaluation
    const active = activeColor()
    // eslint-disable-next-line solid/reactivity -- `lit` is read by the Array.from mapper below, in this same memo evaluation
    const lit = new Set(fillOrder.slice(0, filled()).map(([row, col]) => row * PIP_GRID_COLUMNS + col))
    return Array.from(
      { length: PIP_GRID_PIPS },
      (_, i) => lit.has(i) ? active : 'var(--context-grid-inactive)',
    )
  })

  const tooltip = createMemo(() => {
    const pct = percentage()
    return pct != null ? `Context: ${Math.round(pct)}%` : undefined
  })

  return (
    <Show when={percentage() != null} fallback={<Info size={props.size} />}>
      <Tooltip text={tooltip()} ariaLabel>
        <PipGrid size={props.size} fills={fills()} testId="context-usage-grid" />
      </Tooltip>
    </Show>
  )
}
