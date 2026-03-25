import type { AgentInfo } from '~/generated/leapmux/v1/agent_pb'
import type { AgentSessionInfo } from '~/stores/agentSession.store'
import Check from 'lucide-solid/icons/check'
import Copy from 'lucide-solid/icons/copy'
import { createMemo, createSignal, For, onCleanup, Show } from 'solid-js'
import { AgentProviderIcon, agentProviderLabel } from '~/components/common/AgentProviderIcon'
import { Icon } from '~/components/common/Icon'
import { Tooltip } from '~/components/common/Tooltip'
import { formatCountdown, formatResetTimestamp, getResetsAt, pickUrgentRateLimit, RATE_LIMIT_POPOVER_LABELS } from '~/lib/rateLimitUtils'
import * as styles from './ChatView.css'
import { computePercentage, contextBufferPct, contextSize, resolveContextWindow } from './ContextUsageGrid'
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

function useCopyButton(getText: () => string | undefined) {
  const [copied, setCopied] = createSignal(false)
  const handleCopy = async () => {
    const text = getText()
    if (!text)
      return
    try {
      await navigator.clipboard.writeText(text)
      setCopied(true)
      setTimeout(setCopied, 2000, false)
    }
    catch {
      // ignore clipboard errors
    }
  }
  return { copied, handleCopy }
}

function CopyButton(props: { getText: () => string | undefined, title: string, testId?: string }) {
  const { copied, handleCopy } = useCopyButton(() => props.getText())
  return (
    <button
      class={styles.infoCopyButton}
      onClick={handleCopy}
      title={props.title}
      data-testid={props.testId}
    >
      <Show when={copied()} fallback={<Icon icon={Copy} size="xs" />}>
        <Icon icon={Check} size="xs" />
      </Show>
    </button>
  )
}

export function useAgentInfoCard(props: AgentInfoCardProps) {
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
      <Show when={props.agent?.agentProvider != null}>
        <div class={styles.infoRow} data-testid="info-row-agent-type">
          <span class={styles.infoLabel}>Agent</span>
          <span class={styles.infoValueText} style={{ 'display': 'inline-flex', 'align-items': 'center', 'gap': 'var(--space-1)' }}>
            <AgentProviderIcon provider={props.agent!.agentProvider} size={12} />
            {agentProviderLabel(props.agent!.agentProvider)}
          </span>
        </div>
      </Show>
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
          <CopyButton
            getText={() => props.agent?.agentSessionId}
            title="Copy session ID"
            testId="session-id-copy"
          />
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
          <CopyButton
            getText={() => props.agent!.gitStatus!.branch}
            title="Copy branch name"
          />
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
                <span class={styles.infoValueText}>{flags.join(', ')}</span>
              </div>
            </Show>
          )
        })()}
      </Show>
      <Show when={props.agent?.workingDir}>
        <div class={styles.infoRow} data-testid="info-row-directory">
          <span class={styles.infoLabel}>Directory</span>
          <span class={styles.infoValue}>{tildify(props.agent!.workingDir!, props.agent!.homeDir)}</span>
          <CopyButton
            getText={() => props.agent!.workingDir!}
            title="Copy directory path"
          />
        </div>
      </Show>
      <Show when={props.agentSessionInfo?.planFilePath}>
        <div class={styles.infoRow} data-testid="info-row-plan-file">
          <span class={styles.infoLabel}>Plan File</span>
          <span class={styles.infoValue}>
            {tildify(props.agentSessionInfo!.planFilePath!, props.agent?.homeDir)}
          </span>
          <CopyButton
            getText={() => props.agentSessionInfo!.planFilePath!}
            title="Copy plan file path"
          />
        </div>
      </Show>
      <Show when={props.agentSessionInfo?.contextUsage}>
        {(() => {
          const usage = props.agentSessionInfo!.contextUsage!
          const modelCtxWindow = props.agent?.availableModels?.find(m => m.id === props.agent?.model)?.contextWindow
          const ctxWindow = resolveContextWindow(usage, Number(modelCtxWindow) || undefined)
          const total = contextSize(usage)
          const pct = computePercentage(usage, Number(modelCtxWindow) || undefined, props.agent?.agentProvider)
          const bufferPct = contextBufferPct(props.agent?.agentProvider)
          return (
            <div class={styles.infoRow}>
              <span class={styles.infoLabel}>Context</span>
              <span class={styles.infoValueText}>
                {formatTokenCount(total)}
                {` / ${formatTokenCount(ctxWindow)}`}
                {pct != null ? ` (${Math.round(pct)}%${bufferPct > 0 ? ` with ${bufferPct}% headroom` : ''})` : ''}
              </span>
            </div>
          )
        })()}
      </Show>
      <Show when={props.agentSessionInfo?.totalCostUsd != null}>
        <div class={styles.infoRow}>
          <span class={styles.infoLabel}>Cost</span>
          <span class={styles.infoValueText}>
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

            const status = info.status
            const exceeded = !!status && status !== 'allowed' && status !== 'allowed_warning'
            const resetsAt = getResetsAt(info)

            const statusParts: string[] = []
            if (status === 'allowed')
              statusParts.push('Allowed')
            else if (status === 'allowed_warning')
              statusParts.push('Warning')
            else if (exceeded)
              statusParts.push('Exceeded')
            if (typeof info.utilization === 'number' && !exceeded)
              statusParts.push(`${Math.round(info.utilization * 100)}% used`)
            if (info.isUsingOverage)
              statusParts.push('overage')

            const countdown = typeof resetsAt === 'number' ? formatCountdown(resetsAt) : null

            return (
              <div class={styles.infoRow}>
                <span class={styles.infoLabel}>{typeLabel}</span>
                <span class={styles.infoValueText}>
                  {statusParts.length > 0 ? statusParts.join(', ') : 'Unknown'}
                  <Show when={countdown}>
                    {', '}
                    <Tooltip text={typeof resetsAt === 'number' ? formatResetTimestamp(resetsAt) : undefined}>
                      <span>{`resets in ${countdown}`}</span>
                    </Tooltip>
                  </Show>
                </span>
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
