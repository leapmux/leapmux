import type { Component } from 'solid-js'
import type { ContextUsageInfo } from '~/stores/agentSession.store'
import Info from 'lucide-solid/icons/info'
import { createMemo, For, Show } from 'solid-js'

interface ContextUsageGridProps {
  contextUsage?: ContextUsageInfo
  size: number
}

export const DEFAULT_BUFFER_PCT = 16.5
const DEFAULT_CONTEXT_WINDOW = 200_000

/** Total context size = cache_creation + cache_read + input tokens. */
export function contextSize(usage: ContextUsageInfo): number {
  return usage.cacheCreationInputTokens + usage.cacheReadInputTokens + usage.inputTokens
}

/**
 * Compute context usage percentage from structured token data.
 * Accounts for the autocompact buffer: usable capacity = contextWindow * (1 - buffer%).
 * Falls back to DEFAULT_CONTEXT_WINDOW when contextWindow is not yet known
 * (assistant messages arrive before the result message that carries contextWindow).
 */
export function computePercentage(usage: ContextUsageInfo | undefined): number | null {
  if (!usage)
    return null
  const total = contextSize(usage)
  if (total <= 0)
    return null
  const contextWindow = (usage.contextWindow && usage.contextWindow > 0) ? usage.contextWindow : DEFAULT_CONTEXT_WINDOW
  const usable = contextWindow * (1 - DEFAULT_BUFFER_PCT / 100)
  if (usable <= 0)
    return null
  return Math.min(100, (total / usable) * 100)
}

/** Map a percentage (0-100) to the number of filled squares (0-9). */
function filledCount(pct: number): number {
  if (pct <= 0)
    return 0
  if (pct >= 81)
    return 9
  return Math.ceil(pct / 10)
}

// Fill order: bottom-left to top-right (row 2 L-R, row 1 L-R, row 0 L-R).
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

const SQUARE_SIZE = 3
const GAP = 1
const STEP = SQUARE_SIZE + GAP // 4

export const ContextUsageGrid: Component<ContextUsageGridProps> = (props) => {
  const percentage = createMemo(() => computePercentage(props.contextUsage))

  const filled = createMemo(() => {
    const pct = percentage()
    return pct != null ? filledCount(pct) : 0
  })

  const warning = createMemo(() => (percentage() ?? 0) >= 91)

  const activeColor = () => warning() ? 'var(--context-grid-warning)' : 'currentColor'

  const tooltip = createMemo(() => {
    const pct = percentage()
    return pct != null ? `Context: ${Math.round(pct)}%` : undefined
  })

  return (
    <Show when={percentage() != null} fallback={<Info size={props.size} />}>
      <svg
        width={props.size}
        height={props.size}
        viewBox="0 0 11 11"
        fill="none"
        aria-label={tooltip()}
      >
        <title>{tooltip()}</title>
        <For each={fillOrder}>
          {([row, col], i) => (
            <rect
              x={col * STEP}
              y={row * STEP}
              width={SQUARE_SIZE}
              height={SQUARE_SIZE}
              rx={0.5}
              fill={i() < filled() ? activeColor() : 'var(--context-grid-inactive)'}
            />
          )}
        </For>
      </svg>
    </Show>
  )
}
