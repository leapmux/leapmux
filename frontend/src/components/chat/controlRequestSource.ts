import type { Accessor } from 'solid-js'
import type { MessageContextResolver, ResolvedMessage } from './messageContextResolver'
import type { AgentProvider } from '~/generated/proto/leapmux/v1/agent_pb'
import type { ParsedMessageContent } from '~/lib/messageParser'
import type { ControlRequest } from '~/stores/control.store'
import { createEffect, createMemo, createSignal, on, onCleanup } from 'solid-js'
import { createLogger } from '~/lib/logger'
import { requestInstanceId } from '~/stores/control.store'

const log = createLogger('controlRequestSource')

/** How many times the source loads one request's details before it gives up. */
const MAX_LOAD_ATTEMPTS = 3

/** The delay before each retry, in milliseconds, indexed by the failures so far. */
const LOAD_RETRY_DELAYS_MS = [400, 1600]

/** True for the rejection that states the load STOPPED, rather than failed. */
function isAbortError(error: unknown): boolean {
  return typeof error === 'object' && error !== null && (error as { name?: unknown }).name === 'AbortError'
}

interface SourceQuery {
  key: string
  seq: bigint | undefined
  context: MessageContextResolver | undefined
  provider: AgentProvider | undefined
  loaded: ResolvedMessage | undefined
  agentSessionId: string
}

function sameQuery(first: SourceQuery, second: SourceQuery): boolean {
  return first.key === second.key && first.seq === second.seq && first.context === second.context
    && first.agentSessionId === second.agentSessionId
    && first.provider === second.provider && first.loaded?.message === second.loaded?.message
    && first.loaded?.original === second.loaded?.original
    && first.loaded?.revision.contentVersion === second.loaded?.revision.contentVersion
    && first.loaded?.revision.supplementalRevision === second.loaded?.revision.supplementalRevision
}

/** Resolve control details through the same message reader that tool renderers use. */
export function useControlRequestSource(
  request: Accessor<ControlRequest | null | undefined>,
  context: Accessor<MessageContextResolver | undefined>,
  provider: Accessor<AgentProvider | undefined>,
): Accessor<ParsedMessageContent | undefined> {
  createEffect(() => {
    const current = request()
    const resolver = context()
    const seq = current?.sourceSeq
    if (resolver && seq && seq > 0n)
      onCleanup(resolver.retainMessage(seq))
  })
  const query = createMemo<SourceQuery>(() => {
    const current = request()
    const seq = current?.sourceSeq
    const resolver = context()
    return {
      key: current ? `${current.agentId}:${requestInstanceId(current)}` : '',
      seq,
      context: resolver,
      provider: provider(),
      agentSessionId: current?.agentSessionId ?? '',
      loaded: seq && seq > 0n ? resolver?.peek(seq) : undefined,
    }
  }, { key: '', seq: undefined, context: undefined, provider: undefined, loaded: undefined, agentSessionId: '' }, { equals: sameQuery })

  // A failed load retries a bounded number of times. Without the retry, one
  // transport failure leaves the card without its details for as long as the card
  // stays open: the query is a VALUE identity, so a reconnect that re-delivers the
  // same request reproduces the same key, and nothing re-runs this effect.
  const [retryTick, setRetryTick] = createSignal(0)
  let attempts = { key: '', failures: 0 }
  createEffect(on(
    () => {
      // The tick belongs in the DEPENDENCIES. `on` runs its body untracked, so a
      // read there can never wake this effect again.
      retryTick()
      return query()
    },
    ({ key, seq, context: resolver, provider: agentProvider, loaded }) => {
      let active = true
      let retryTimer: ReturnType<typeof setTimeout> | undefined
      onCleanup(() => {
        active = false
        if (retryTimer !== undefined)
          clearTimeout(retryTimer)
      })
      // Each request gets its own budget, so a new card never inherits the
      // failures of the one before it.
      if (attempts.key !== key)
        attempts = { key, failures: 0 }
      if (!key || !seq || seq <= 0n || !resolver || agentProvider === undefined) {
        return
      }
      if (loaded) {
        // The card holds its details. A later loss of them starts a full budget.
        attempts.failures = 0
        return
      }
      void resolver.message(seq).catch((error) => {
        if (!active)
          return
        log.warn('Could not load control details', { sequence: seq.toString(), attempt: attempts.failures + 1, error })
        // An abort states that this query ended, not that the load failed, so it
        // spends no attempt. The resolver aborts when it leaves its scope.
        if (isAbortError(error))
          return
        attempts.failures += 1
        const delay = LOAD_RETRY_DELAYS_MS[attempts.failures - 1]
        if (attempts.failures >= MAX_LOAD_ATTEMPTS || delay === undefined)
          return
        retryTimer = setTimeout(() => setRetryTick(value => value + 1), delay)
      })
    },
  ))

  return () => {
    const { seq, context: resolver, provider: agentProvider, loaded, agentSessionId } = query()
    if (!seq || seq <= 0n || !resolver)
      return undefined
    if (loaded && loaded.message.agentProvider === agentProvider && loaded.message.agentSessionId === agentSessionId)
      return loaded.parsed
    return undefined
  }
}
