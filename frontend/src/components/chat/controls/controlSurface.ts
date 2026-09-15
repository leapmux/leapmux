import type { Accessor } from 'solid-js'
import type { MessageContextResolver } from '../messageContextResolver'
import type { ControlQuestion } from './AskUserQuestionControl'
import type { ElicitationRequest } from './elicitationForm'
import type { AgentProvider } from '~/generated/proto/leapmux/v1/agent_pb'
import type { ParsedMessageContent } from '~/lib/messageParser'
import type { ControlRequest } from '~/stores/control.store'
import { createMemo } from 'solid-js'
import { controlRequestProvider } from '~/stores/control.store'
import { useControlRequestSource } from '../controlRequestSource'
import { pluginFor } from '../providers/registry'
import { controlQuestion } from './AskUserQuestionControl'

/**
 * Which control surface answers ONE request: the question form, the elicitation
 * form, or the provider's own plugin.
 *
 * `plugin` is the answer for everything else, and it carries no payload of its
 * own: the banner renders the plugin's components and the composer asks the
 * plugin for its editor purpose.
 */
export type ControlSurface
  = | { kind: 'question', question: ControlQuestion }
    | { kind: 'elicitation', elicitation: ElicitationRequest }
    | { kind: 'plugin' }

/**
 * The ONE classifier. The banner and the composer both call it, with the same
 * three inputs, so they cannot reach different answers for the same request.
 *
 * They did. The banner resolved the request's source message and passed it; the
 * composer called the elicitation recognizer with no source at all. A plugin
 * whose recognition reads the source would then draw the elicitation form in the
 * banner while the composer offered the feedback editor beside it.
 *
 * Returns `undefined` for an absent request, which a caller gets whenever the
 * store removes the request it holds.
 */
export function controlSurface(
  request: ControlRequest | null | undefined,
  agentProvider: AgentProvider | undefined,
  source: ParsedMessageContent | undefined,
): ControlSurface | undefined {
  if (!request)
    return undefined
  // Resolved ONCE here, so the two recognizers below cannot read two different
  // plugins for the same request. A caller that already resolved the provider
  // loses nothing: the request's own provider wins either way.
  const provider = controlRequestProvider(request, agentProvider)
  const question = controlQuestion(request, provider, source)
  if (question)
    return { kind: 'question', question }
  const elicitation = pluginFor(provider)?.elicitation?.(request.payload, source)
  return elicitation ? { kind: 'elicitation', elicitation } : { kind: 'plugin' }
}

/** The classification of ONE live control request, as reactive accessors. */
export interface LiveControlSurface {
  /** The provider whose plugin renders the request, resolved against the agent's. */
  provider: Accessor<AgentProvider | undefined>
  /** Which surface answers the request. */
  surface: Accessor<ControlSurface | undefined>
}

/**
 * Classifies ONE live control request, and keeps its source message loaded for
 * as long as the request lives.
 *
 * ONE graph for each request. `ControlRequestContent` and
 * `ControlRequestActions` mount in two different slots for the SAME request,
 * and each built its own graph before this: three source readers, three leases
 * and three payload parses where one answers all of them. The composer derives
 * it once and passes the surface to both as a prop.
 *
 * The caller must keep this OUTSIDE the `<Show when={request}>` that renders
 * the surface, never inside it. A `<Show>` disposes what it owns as soon as its
 * condition turns falsy, so a memo inside it is gone before anything can read
 * the surface of the request that the Show removed. `controlSurface` accepts an
 * absent request for the same reason: a caller that passes `request` as a
 * reactive prop re-runs this memo with the removed request.
 */
export function createControlSurface(
  request: Accessor<ControlRequest | null | undefined>,
  messageContext: Accessor<MessageContextResolver | undefined>,
  agentProvider: Accessor<AgentProvider | undefined>,
): LiveControlSurface {
  const provider = createMemo(() => controlRequestProvider(request(), agentProvider()))
  const source = useControlRequestSource(request, messageContext, provider)
  const surface = createMemo(() => controlSurface(request(), provider(), source()))
  return { provider, surface }
}
