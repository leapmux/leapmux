import type { Accessor } from 'solid-js'
import type { MessageContextResolver } from './messageContextResolver'
import type { AgentProvider } from '~/generated/proto/leapmux/v1/agent_pb'
import type { ParsedMessageContent } from '~/lib/messageParser'
import type { ControlRequest } from '~/stores/control.store'
import { createEffect, onCleanup, untrack } from 'solid-js'
import { createLogger } from '~/lib/logger'
import { pluginFor } from './providers/registry'

const log = createLogger('controlRequestToolSpan')

/**
 * The tool-REQUEST row of the call one permission request is about.
 *
 * The Agent Client Protocol family states its tool call in a compact form -- an id,
 * a title and a kind -- and the ARGUMENTS live on that call's own transcript row. A
 * banner built from the request alone showed a title above nothing.
 *
 * It lives beside `useControlRequestSource` and for the same reason: keeping a row
 * loaded for as long as a request needs it is lifecycle, not extraction, so it
 * belongs in the surface derivation rather than inside a component that a `<Show>`
 * can dispose mid-load.
 *
 * Returns undefined for a request that identifies no tool call, which is every provider
 * outside that family.
 */
export function useControlRequestToolSpan(
  request: Accessor<ControlRequest | null | undefined>,
  context: Accessor<MessageContextResolver | undefined>,
  provider: Accessor<AgentProvider | undefined>,
): Accessor<ParsedMessageContent | undefined> {
  const identity = (): { spanId: string, agentSessionId: string } | undefined => {
    const current = request()
    // The provider identifies the span, because the path to a tool-call id is that
    // provider's own wire shape.
    const spanId = current ? pluginFor(provider())?.controls?.controlToolSpanId?.(current.payload) : ''
    return spanId ? { spanId, agentSessionId: current?.agentSessionId ?? '' } : undefined
  }

  createEffect(() => {
    const resolver = context()
    const target = identity()
    if (!resolver || !target)
      return
    onCleanup(resolver.retainSpan(target))
    // A row already loaded must not release its own lease and cause a second fetch.
    if (untrack(() => resolver.request(target)))
      return
    void resolver.loadSpan(target).catch((error: unknown) => {
      if (error instanceof DOMException && error.name === 'AbortError')
        return
      log.warn('Could not load the tool call a permission refers to', { spanId: target.spanId, error })
    })
  })

  return () => {
    const resolver = context()
    const target = identity()
    return resolver && target ? resolver.request(target)?.resolved : undefined
  }
}
