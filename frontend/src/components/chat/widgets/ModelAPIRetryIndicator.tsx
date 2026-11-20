import type { Component } from 'solid-js'
import type { LiveModelAPIRetry } from '~/stores/agentSession.store'
import { Show } from 'solid-js'
import { MODEL_API_RETRY_PHASE } from '~/generated/contracts/session-info'
import { resultDivider } from '../messageStyles.css'
import { formatShortWait, joinMetaParts } from '../rendererUtils'

function retryLabel(retry: LiveModelAPIRetry): string {
  const count = retry.maxAttempts === undefined
    ? retry.attempt
    : `${retry.attempt}/${retry.maxAttempts}`
  const status = retry.errorStatus === undefined
    ? undefined
    : `HTTP ${retry.errorStatus}`
  const waiting = retry.phase === MODEL_API_RETRY_PHASE.Waiting
  const delay = waiting && retry.retryDelayMs
    ? ` ${formatShortWait(retry.retryDelayMs)}`
    : ''
  const phase = waiting
    ? `Waiting${delay}`
    : 'Running'
  return joinMetaParts([
    `Model API retry ${count}`,
    status,
    phase,
    retry.error,
  ])
}

export const ModelAPIRetryIndicator: Component<{ retry?: LiveModelAPIRetry | undefined }> = props => (
  <Show when={props.retry}>
    {retry => (
      <div class={resultDivider} data-testid="model-api-retry-indicator" role="status">
        {retryLabel(retry())}
      </div>
    )}
  </Show>
)
