import type { Accessor } from 'solid-js'
import type { MessageContextResolver, ResolvedMessage } from './messageContextResolver'
import type { AgentProvider } from '~/generated/proto/leapmux/v1/agent_pb'
import type { ParsedMessageContent } from '~/lib/messageParser'
import type { ControlRequest } from '~/stores/control.store'
import { createEffect, createMemo, createSignal, on, onCleanup } from 'solid-js'
import { createLogger } from '~/lib/logger'
import { requestInstanceId } from '~/stores/control.store'

const log = createLogger('controlRequestSource')

interface SourceState {
  key: string
  seq: bigint
  context: MessageContextResolver
  message: ResolvedMessage
}

interface SourceQuery {
  key: string
  seq: bigint | undefined
  context: MessageContextResolver | undefined
  provider: AgentProvider | undefined
  loaded: ResolvedMessage | undefined
}

function sameQuery(first: SourceQuery, second: SourceQuery): boolean {
  return first.key === second.key && first.seq === second.seq && first.context === second.context
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
  const [fetched, setFetched] = createSignal<SourceState>()
  const query = createMemo<SourceQuery>(() => {
    const current = request()
    const seq = current?.sourceSeq
    const resolver = context()
    return {
      key: current ? `${current.agentId}:${requestInstanceId(current)}` : '',
      seq,
      context: resolver,
      provider: provider(),
      loaded: seq && seq > 0n ? resolver?.peek(seq) : undefined,
    }
  }, { key: '', seq: undefined, context: undefined, provider: undefined, loaded: undefined }, { equals: sameQuery })

  createEffect(on(
    query,
    ({ key, seq, context: resolver, provider: agentProvider, loaded }) => {
      let active = true
      onCleanup(() => {
        active = false
      })
      if (!key || !seq || seq <= 0n || !resolver || agentProvider === undefined) {
        setFetched(undefined)
        return
      }
      if (loaded) {
        setFetched(loaded.message.agentProvider === agentProvider ? { key, seq, context: resolver, message: loaded } : undefined)
        return
      }
      const previous = fetched()
      if (previous?.key === key && previous.seq === seq && previous.context === resolver && previous.message.message.agentProvider === agentProvider)
        return
      setFetched(undefined)
      void resolver.message(seq).then((message) => {
        if (active && message && message.message.agentProvider === agentProvider)
          setFetched({ key, seq, context: resolver, message })
      }).catch((error) => {
        if (active)
          log.warn('Could not load control details', { sequence: seq.toString(), error })
      })
    },
  ))

  return () => {
    const { key, seq, context: resolver, provider: agentProvider, loaded } = query()
    if (!seq || seq <= 0n || !resolver)
      return undefined
    if (loaded && loaded.message.agentProvider === agentProvider)
      return loaded.parsed
    const stored = fetched()
    return stored?.key === key && stored.seq === seq && stored.context === resolver
      && stored.message.message.agentProvider === agentProvider
      ? stored.message.parsed
      : undefined
  }
}
