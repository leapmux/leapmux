import type { AgentInfo } from '~/generated/leapmux/v1/agent_pb'
import type { AgentSessionInfo } from '~/stores/agentSession.store'
import Check from 'lucide-solid/icons/check'
import Copy from 'lucide-solid/icons/copy'
import { createMemo, createSignal, For, onCleanup, Show } from 'solid-js'
import { Icon } from '~/components/common/Icon'
import { formatRateLimitSummary, pickUrgentRateLimit, RATE_LIMIT_POPOVER_LABELS } from '~/lib/rateLimitUtils'
import * as styles from './ChatView.css'
import { computePercentage, contextSize, DEFAULT_BUFFER_PCT } from './ContextUsageGrid'
import { tildify } from './messageUtils'

export interface AgentInfoCardProps {
  agent?: AgentInfo
  agentSessionInfo?: AgentSessionInfo
}

function formatTokenCount(tokens: number): string {
  if (tokens >= 1_000_000)
    return `${(tokens / 1_000_000).toFixed(1)}M`
  if (tokens >= 1_000)
    return `${(tokens / 1_000).toFixed(1)}k`
  return String(tokens)
}

export function useAgentInfoCard(props: AgentInfoCardProps) {
  const [sessionIdCopied, setSessionIdCopied] = createSignal(false)

  const handleCopySessionId = async () => {
    const sid = props.agent?.agentSessionId
    if (!sid)
      return
    try {
      await navigator.clipboard.writeText(sid)
      setSessionIdCopied(true)
      setTimeout(() => setSessionIdCopied(false), 2000)
    }
    catch {
      // ignore clipboard errors
    }
  }

  const hasContextInfo = () => {
    return props.agentSessionInfo?.totalCostUsd != null
      || props.agentSessionInfo?.contextUsage
      || (props.agentSessionInfo?.rateLimits && Object.keys(props.agentSessionInfo.rateLimits).length > 0)
  }

  const showInfoTrigger = () => !!props.agent?.agentSessionId || hasContextInfo()

  // 1-minute timer for countdown refresh
  const [now, setNow] = createSignal(Date.now())
  const timer = setInterval(() => setNow(Date.now()), 60_000)
  onCleanup(() => clearInterval(timer))

  // Derive urgent rate limit (re-evaluates each minute due to `now()` dependency)
  const urgentRateLimit = createMemo(() => {
    void now() // subscribe to timer ticks
    const rateLimits = props.agentSessionInfo?.rateLimits
    if (!rateLimits)
      return null
    return pickUrgentRateLimit(rateLimits)
  })

  const infoHoverCardContent = () => (
    <>
      <Show when={props.agent?.workerName}>
        <div class={styles.infoRow} data-testid="info-row-worker">
          <span class={styles.infoLabel}>Worker</span>
          <span class={styles.infoValue}>{props.agent!.workerName}</span>
        </div>
      </Show>
      <Show when={props.agent?.agentSessionId}>
        <div class={styles.infoRow}>
          <span class={styles.infoLabel}>Session ID</span>
          <span class={styles.infoValue} data-testid="session-id-value">{props.agent?.agentSessionId}</span>
          <button
            class={styles.infoCopyButton}
            onClick={handleCopySessionId}
            title="Copy session ID"
            data-testid="session-id-copy"
          >
            <Show when={sessionIdCopied()} fallback={<Icon icon={Copy} size="xs" />}>
              <Icon icon={Check} size="xs" />
            </Show>
          </button>
        </div>
      </Show>
      <Show when={props.agent?.gitStatus?.branch}>
        <div class={styles.infoRow}>
          <span class={styles.infoLabel}>Branch</span>
          <span class={styles.infoValue}>
            {props.agent!.gitStatus!.branch}
            {(() => {
              const gs = props.agent!.gitStatus!
              const parts: string[] = []
              if (gs.ahead)
                parts.push(`+${gs.ahead}`)
              if (gs.behind)
                parts.push(`-${gs.behind}`)
              return parts.length > 0 ? ` [${parts.join(' ')}]` : ''
            })()}
          </span>
        </div>
        {(() => {
          const gs = props.agent!.gitStatus!
          const flags: string[] = []
          if (gs.conflicted)
            flags.push('Conflicted')
          if (gs.stashed)
            flags.push('Stashed')
          if (gs.modified)
            flags.push('Modified')
          if (gs.added)
            flags.push('Added')
          if (gs.deleted)
            flags.push('Deleted')
          if (gs.renamed)
            flags.push('Renamed')
          if (gs.typeChanged)
            flags.push('Type-changed')
          if (gs.untracked)
            flags.push('Untracked')
          return (
            <Show when={flags.length > 0}>
              <div class={styles.infoRow}>
                <span class={styles.infoLabel}>Status</span>
                <span class={styles.infoValue}>{flags.join(', ')}</span>
              </div>
            </Show>
          )
        })()}
      </Show>
      <Show when={props.agent?.workingDir}>
        <div class={styles.infoRow} data-testid="info-row-directory">
          <span class={styles.infoLabel}>Directory</span>
          <span class={styles.infoValue}>{tildify(props.agent!.workingDir!, props.agent!.homeDir)}</span>
        </div>
      </Show>
      <Show when={props.agentSessionInfo?.planFilePath}>
        <div class={styles.infoRow} data-testid="info-row-plan-file">
          <span class={styles.infoLabel}>Plan File</span>
          <span class={styles.infoValue}>
            {tildify(props.agentSessionInfo!.planFilePath!, props.agent?.homeDir)}
          </span>
        </div>
      </Show>
      <Show when={props.agentSessionInfo?.contextUsage}>
        {(() => {
          const usage = props.agentSessionInfo!.contextUsage!
          const ctxWindow = (usage.contextWindow && usage.contextWindow > 0) ? usage.contextWindow : 200_000
          const total = contextSize(usage)
          const pct = computePercentage(usage)
          return (
            <div class={styles.infoRow}>
              <span class={styles.infoLabel}>Context</span>
              <span class={styles.infoValue}>
                {formatTokenCount(total)}
                {` / ${formatTokenCount(ctxWindow)}`}
                {pct != null ? ` (${Math.round(pct)}% with ${DEFAULT_BUFFER_PCT}% buffer)` : ''}
              </span>
            </div>
          )
        })()}
      </Show>
      <Show when={props.agentSessionInfo?.totalCostUsd != null}>
        <div class={styles.infoRow}>
          <span class={styles.infoLabel}>Cost</span>
          <span class={styles.infoValue}>
            $
            {props.agentSessionInfo!.totalCostUsd!.toFixed(4)}
          </span>
        </div>
      </Show>
      <Show when={props.agentSessionInfo?.rateLimits && Object.keys(props.agentSessionInfo!.rateLimits!).length > 0}>
        <For each={Object.values(props.agentSessionInfo!.rateLimits!)}>
          {(info) => {
            const typeLabel = RATE_LIMIT_POPOVER_LABELS[info.rateLimitType ?? '']
              ?? (info.rateLimitType ? `Rate Limit (${info.rateLimitType})` : 'Rate Limit')
            return (
              <div class={styles.infoRow}>
                <span class={styles.infoLabel}>{typeLabel}</span>
                <span class={styles.infoValue}>{formatRateLimitSummary(info)}</span>
              </div>
            )
          }}
        </For>
      </Show>
    </>
  )

  return {
    infoHoverCardContent,
    showInfoTrigger,
    urgentRateLimit,
  }
}
