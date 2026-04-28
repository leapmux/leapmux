import type { Component } from 'solid-js'
import type { AgentProvider } from '~/generated/leapmux/v1/agent_pb'
import type { ContextUsageInfo } from '~/stores/agentSession.store'
import Info from 'lucide-solid/icons/info'
import { createMemo, For, Show } from 'solid-js'
import { Tooltip } from '~/components/common/Tooltip'
import { AgentProvider as AgentProviderEnum } from '~/generated/leapmux/v1/agent_pb'

interface ContextUsageGridProps {
  contextUsage?: ContextUsageInfo
  modelContextWindow?: number
  agentProvider?: AgentProvider
  size: number
}

export const DEFAULT_BUFFER_PCT = 16.5
export const DEFAULT_CONTEXT_WINDOW = 200_000

export function contextBufferPct(agentProvider?: AgentProvider): number {
  return agentProvider === AgentProviderEnum.CLAUDE_CODE ? DEFAULT_BUFFER_PCT : 0
}

/** Resolve the effective context window from usage data, model metadata, or the default. */
export function resolveContextWindow(usage: ContextUsageInfo, modelContextWindow?: number): number {
  if (usage.contextWindow && usage.contextWindow > 0)
    return usage.contextWindow
  if (modelContextWindow && modelContextWindow > 0)
    return modelContextWindow
  return DEFAULT_CONTEXT_WINDOW
}

/** Total context size = cache_creation + cache_read + input tokens. */
export function contextSize(usage: ContextUsageInfo): number {
  return usage.cacheCreationInputTokens + usage.cacheReadInputTokens + usage.inputTokens
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
  const percentage = createMemo(() => computePercentage(props.contextUsage, props.modelContextWindow, props.agentProvider))

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
      <Tooltip text={tooltip()} ariaLabel>
        <svg
          width={props.size}
          height={props.size}
          viewBox="0 0 11 11"
          fill="none"
        >
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
      </Tooltip>
    </Show>
  )
}
