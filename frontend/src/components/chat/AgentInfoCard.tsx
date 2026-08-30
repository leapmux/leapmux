import type { AgentInfo, AgentProvider } from '~/generated/proto/leapmux/v1/agent_pb'
import type { AgentSessionInfo } from '~/stores/agentSession.store'
import type { RepoGitView } from '~/stores/repoGit'
import Check from 'lucide-solid/icons/check'
import Copy from 'lucide-solid/icons/copy'
import { createMemo, createSignal, For, onCleanup, Show } from 'solid-js'
import { AgentProviderIcon, agentProviderLabel } from '~/components/common/AgentProviderIcon'
import { Icon } from '~/components/common/Icon'
import { Tooltip } from '~/components/common/Tooltip'
import { useCopyButton } from '~/hooks/useCopyButton'
import { basename, tildify } from '~/lib/paths'
import { formatCountdown, formatResetTimestamp, getResetsAt, pickUrgentRateLimit, RATE_LIMIT_POPOVER_LABELS } from '~/lib/rateLimitUtils'
import * as styles from './ChatView.css'
import { pluginFor } from './providers/registry'
import { formatTokenCount } from './rendererUtils'
import { OPTION_ID_MODEL, optionGroup, selectedModelContextWindow } from './settingsGroups'
import { computePercentage, contextBufferPct, contextSize, resolveContextWindow } from './widgets/ContextUsageGrid'

export interface AgentInfoCardProps {
  agent?: AgentInfo
  agentSessionInfo?: AgentSessionInfo
  /** Branch label from {@link repoGitView}. */
  branchName?: string
  /** Git flags and ahead/behind from {@link repoGitView}. */
  gitView?: RepoGitView
}

export function formatAgentSessionIdForDisplay(agentProvider: AgentProvider | undefined, sessionId: string): string {
  if (!pluginFor(agentProvider)?.sessionIdIsFilePath)
    return sessionId

  const tail = basename(sessionId) || sessionId
  return tail.endsWith('.jsonl') ? tail.slice(0, -'.jsonl'.length) : tail
}

function CopyButton(props: { getText: () => string | undefined, title: string, testId?: string }) {
  const { copied, copy: handleCopy } = useCopyButton(() => props.getText())
  return (
    <Tooltip text={props.title} ariaLabel>
      <button
        class={styles.infoCopyButton}
        onClick={handleCopy}
        data-testid={props.testId}
      >
        <Show when={copied()} fallback={<Icon icon={Copy} size="xs" />}>
          <Icon icon={Check} size="xs" />
        </Show>
      </button>
    </Tooltip>
  )
}

export function useAgentInfoCard(props: AgentInfoCardProps) {
  /**
   * ONE snapshot per prop, read through a memo.
   *
   * `agent` and `agentSessionInfo` are re-resolved on every read upstream
   * (`getInfo(focusedAgentId())`, `agentTabToInfo(getAgentTab(id))` -- the
   * latter allocates a fresh object each call), so two reads inside one render
   * could answer with two DIFFERENT agents. That is how the Context row came to
   * dereference undefined: its <Show> guard saw one agent's info and its body
   * the next one's.
   *
   * A memo read is guaranteed to return the same value for the whole pass, so
   * every guard and every body below reads `agent()` / `sessionInfo()` and
   * never `props.*`. That makes the card total by construction rather than by a
   * discipline each new row has to remember.
   */
  const agent = createMemo(() => props.agent)
  const sessionInfo = createMemo(() => props.agentSessionInfo)
  const gitView = createMemo(() => props.gitView)
  const branchLabel = createMemo(() => props.branchName)

  const hasContextInfo = () => {
    const info = sessionInfo()
    return info?.totalCostUsd != null
      || !!info?.contextUsage
      || Object.keys(info?.rateLimits ?? {}).length > 0
  }

  const showInfoTrigger = () => !!agent()?.agentSessionId || hasContextInfo()

  const sessionIdDisplay = createMemo(() => {
    const sessionId = agent()?.agentSessionId
    return sessionId ? formatAgentSessionIdForDisplay(agent()?.agentProvider, sessionId) : undefined
  })
  const sessionIdCopyTitle = () => pluginFor(agent()?.agentProvider)?.sessionIdIsFilePath ? 'Copy session file path' : 'Copy session ID'

  // 1-minute timer for countdown refresh
  const [now, setNow] = createSignal(Date.now())
  const timer = setInterval(() => setNow(Date.now()), 60_000)
  onCleanup(() => clearInterval(timer))

  // The rate-limit rows as a plain array. Derived once so the guard and the
  // <For> read the SAME list instead of each re-resolving `rateLimits` off a
  // prop that answers differently per read.
  const rateLimitList = createMemo(() => Object.values(sessionInfo()?.rateLimits ?? {}))

  // The provider WRAPPED IN AN OBJECT, so a keyed <Show> can carry it.
  //
  // AgentProvider is a numeric enum whose zero value (UNSPECIFIED) is a real
  // answer that agentProviderLabel renders as "Unknown", and `<Show when={0}>`
  // is falsy -- so passing the raw value would hide the row for it. Wrapping
  // makes the guard total: present is always truthy, absent is undefined. The
  // prop is read ONCE here rather than in both the guard and the body, which is
  // the property the whole card is built on.
  const agentProviderRow = createMemo(() => {
    const provider = agent()?.agentProvider
    return provider == null ? undefined : { provider }
  })

  // Derive urgent rate limit (re-evaluates each minute due to `now()` dependency)
  const urgentRateLimit = createMemo(() => {
    void now() // subscribe to timer ticks
    const rateLimits = sessionInfo()?.rateLimits
    if (!rateLimits)
      return null
    return pickUrgentRateLimit(rateLimits)
  })

  const infoHoverCardContent = () => (
    <>
      {/* Every row reads `agent()` / `sessionInfo()`, never `props.*`. The two
          memos are what make a re-read safe: a memo answers with the same value
          for the whole pass, so a guard and its body cannot see two different
          agents. The keyed callbacks below are the second layer -- they hand the
          body the exact value the guard admitted, which keeps a row total even
          if someone later reaches past the memo. */}
      <Show when={agentProviderRow()} keyed>
        {row => (
          <div class={styles.infoRow} data-testid="info-row-agent-type">
            <span class={styles.infoLabel}>Agent</span>
            <span class={styles.infoValueText} style={{ 'display': 'inline-flex', 'align-items': 'center', 'gap': 'var(--space-1)' }}>
              <AgentProviderIcon provider={row.provider} size={12} />
              {agentProviderLabel(row.provider)}
            </span>
          </div>
        )}
      </Show>
      <Show when={agent()?.workerName} keyed>
        {workerName => (
          <div class={styles.infoRow} data-testid="info-row-worker">
            <span class={styles.infoLabel}>Worker</span>
            <span class={styles.infoValue}>{workerName}</span>
          </div>
        )}
      </Show>
      <Show when={agent()?.agentSessionId}>
        <div class={styles.infoRow}>
          <span class={styles.infoLabel}>Session ID</span>
          <span class={styles.infoValue} data-testid="session-id-value">{sessionIdDisplay()}</span>
          <CopyButton
            getText={() => agent()?.agentSessionId}
            title={sessionIdCopyTitle()}
            testId="session-id-copy"
          />
        </div>
      </Show>
      <Show when={branchLabel()} keyed>
        {name => (
          <div class={styles.infoRow}>
            <span class={styles.infoLabel}>Branch</span>
            <span class={styles.infoValue}>
              {name}
              {(() => {
                const git = gitView()
                const parts: string[] = []
                if (git?.ahead)
                  parts.push(`+${git.ahead}`)
                if (git?.behind)
                  parts.push(`-${git.behind}`)
                return parts.length > 0 ? ` [${parts.join(' ')}]` : ''
              })()}
            </span>
            <CopyButton
              getText={() => name}
              title="Copy branch name"
            />
          </div>
        )}
      </Show>
      {(() => {
        const git = gitView()
        const flags: string[] = []
        if (git?.conflicted)
          flags.push('Conflicted')
        if (git?.stashed)
          flags.push('Stashed')
        if (git?.modified)
          flags.push('Modified')
        if (git?.added)
          flags.push('Added')
        if (git?.deleted)
          flags.push('Deleted')
        if (git?.renamed)
          flags.push('Renamed')
        if (git?.typeChanged)
          flags.push('Type-changed')
        if (git?.untracked)
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
      <Show when={agent()?.workingDir} keyed>
        {workingDir => (
          <div class={styles.infoRow} data-testid="info-row-directory">
            <span class={styles.infoLabel}>Directory</span>
            <span class={styles.infoValue}>{tildify(workingDir, agent()?.homeDir)}</span>
            <CopyButton
              getText={() => workingDir}
              title="Copy directory path"
            />
          </div>
        )}
      </Show>
      <Show when={sessionInfo()?.planFilePath} keyed>
        {planFilePath => (
          <div class={styles.infoRow} data-testid="info-row-plan-file">
            <span class={styles.infoLabel}>Plan File</span>
            <span class={styles.infoValue}>
              {tildify(planFilePath, agent()?.homeDir)}
            </span>
            <CopyButton
              getText={() => planFilePath}
              title="Copy plan file path"
            />
          </div>
        )}
      </Show>
      {/* The row this card was rewritten for. `keyed` + a callback body, not a
          bare `<Show>` around a re-read: the body dereferenced
          `undefined.contextWindow` when a focus switch answered the guard with
          one agent's info and the body with the next agent's. The memo now
          rules that out for the whole pass, and the keyed callback keeps the
          body reading a value it was handed rather than one it looked up. */}
      <Show when={sessionInfo()?.contextUsage} keyed>
        {(usage) => {
          const currentModel = optionGroup(agent()?.optionGroups, OPTION_ID_MODEL)?.currentValue || ''
          const modelCtxWindow = selectedModelContextWindow(agent()?.optionGroups, currentModel) || undefined
          const ctxWindow = resolveContextWindow(usage, modelCtxWindow)
          const total = contextSize(usage)
          const pct = computePercentage(usage, modelCtxWindow, agent()?.agentProvider)
          const bufferPct = contextBufferPct(agent()?.agentProvider)
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
        }}
      </Show>
      {/* Not keyed: a real cost of 0 is falsy, and Show would hide the row.
          The guard stays a null test and the body is total instead. */}
      <Show when={sessionInfo()?.totalCostUsd != null}>
        <div class={styles.infoRow}>
          <span class={styles.infoLabel}>Cost</span>
          <span class={styles.infoValueText}>
            $
            {(sessionInfo()?.totalCostUsd ?? 0).toFixed(4)}
          </span>
        </div>
      </Show>
      {/* A plain boolean guard, NOT `keyed` on the list. rateLimitList is a
          memo that allocates a fresh array each run, so keying on it would
          rebuild the whole <For> subtree on every session-info tick -- the
          reconciliation For exists for could never reuse a row. The memo
          already gives both the guard and the <For> the same list, which is
          what the keyed form was reaching for. */}
      <Show when={rateLimitList().length > 0}>
        <For each={rateLimitList()}>
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
