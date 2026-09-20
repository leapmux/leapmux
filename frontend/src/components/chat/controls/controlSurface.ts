import type { Accessor } from 'solid-js'
import type { MessageContextResolver } from '../messageContextResolver'
import type { ControlPrompt } from '../model/controlPrompt'
import type { ActiveQuestionControl } from './AskUserQuestionControl'
import type { AgentProvider } from '~/generated/proto/leapmux/v1/agent_pb'
import type { ParsedMessageContent } from '~/lib/messageParser'
import type { ControlRequest } from '~/stores/control.store'
import { createMemo } from 'solid-js'
import { controlRequestProvider } from '~/stores/control.store'
import { useControlRequestSource } from '../controlRequestSource'
import { useControlRequestToolSpan } from '../controlRequestToolSpan'
import { pluginFor } from '../providers/registry'
import { controlQuestion } from './AskUserQuestionControl'

/**
 * Which control surface answers ONE request.
 *
 * It is the shared control model, with the QUESTION variant carrying its send path
 * beside the questions: a question is answered through the provider's own
 * `askUserQuestion` capability, and the banner and the composer both need it.
 * Every other variant is drawn and answered from the model alone.
 */
export type ControlSurface
  = | { kind: 'question', question: ActiveQuestionControl }
    | Exclude<ControlPrompt, { kind: 'question' }>

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
  toolRequest?: ParsedMessageContent,
): ControlSurface | undefined {
  if (!request)
    return undefined
  // Resolved ONCE here, so the two recognizers below cannot read two different
  // plugins for the same request. A caller that already resolved the provider
  // loses nothing: the request's own provider wins either way.
  const provider = controlRequestProvider(request, agentProvider)
  // The question keeps its own path, because the capability it carries is the SEND
  // path and not content: the composer submits through it.
  const question = controlQuestion(request, provider, source)
  if (question)
    return { kind: 'question', question }
  const plugin = pluginFor(provider)
  // The elicitation keeps its own step for the same reason the question does: it is a
  // cross-provider hook with one meaning, every provider registers it, and the
  // composer reads the same hook to pick its editor. Seven copies inside
  // `extractControl` would be seven chances to forget it.
  const elicitation = plugin?.controls?.elicitation?.(request.payload, source)
  if (elicitation)
    return { kind: 'elicitation', elicitation }
  // The optional reads are set only when they hold a message: an absent source
  // and an absent tool span are different facts from explicit `undefined` ones
  // under exactOptionalPropertyTypes, and every extractor treats them the same.
  const extracted = plugin?.controls?.extractControl?.({
    payload: request.payload,
    ...(source === undefined ? {} : { source }),
    ...(toolRequest === undefined ? {} : { request: toolRequest }),
  })
  if (extracted)
    return extracted
  // A provider that reads nothing leaves the generic permission row: the shared
  // Allow/Deny pair, and no arguments. `extractControl` can no longer answer a
  // question -- `ExtractedControlRequest` excludes that variant, so
  // `askUserQuestion.isRequest` above is the one recognizer.
  //
  // It states NO `input`, and that is the whole point of the branch. It used to pass
  // `request.payload`, which is the whole JSON-RPC envelope, so the banner headed it
  // "Arguments" while the Allow button beside it sent `payload.request.input ?? {}`
  // -- the two halves of one banner read two different parts of the payload, and the
  // half the reader saw was not the half the agent received. `ControlJson` hides an
  // empty value, so the banner now draws the decision alone.
  return { kind: 'permission', permission: { options: [] } }
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
  const toolRequest = useControlRequestToolSpan(request, messageContext, provider)
  const surface = createMemo(() => controlSurface(request(), provider(), source(), toolRequest()))
  return { provider, surface }
}
