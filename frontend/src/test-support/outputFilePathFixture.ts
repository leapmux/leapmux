import type { ProviderRowOptions } from './toolCallFixture'
import type { RowExtractionInput } from '~/components/chat/rowExtractionTypes'
import type { AgentProvider } from '~/generated/proto/leapmux/v1/agent_pb'
import { providerFor, resolveMessageForRendering } from '~/components/chat/providers/registry'
import { input } from '~/components/chat/providers/testUtils'
import { isObject } from '~/lib/jsonPick'
import { providerToolCall } from './toolCallFixture'

/** Keep the valid call fixed while the real hook reads each test payload. */
export function outputFilePathFixture(
  provider: AgentProvider,
  original: Record<string, unknown>,
  options: ProviderRowOptions = {},
): (payload?: unknown, changes?: ProviderRowOptions) => readonly string[] {
  const plugin = providerFor(provider)
  const hook = plugin?.transcript.outputFilePaths
  if (!plugin || !hook)
    throw new Error('The output path fixture requires a registered path hook.')
  const defaults = { ...options }
  const call = providerToolCall(provider, original, defaults)
  if (!call)
    throw new Error('The output path fixture requires a valid registered tool call.')
  return (payload = original, changes = {}) => {
    const scoped = { ...defaults, ...changes }
    const resolved = resolveMessageForRendering({
      ...input(isObject(payload) ? payload : undefined, undefined, provider),
      topLevel: isObject(payload) ? payload : null,
      supplementalContent: scoped.supplementalContent,
      ...(scoped.agentSessionId === undefined ? {} : { agentSessionId: scoped.agentSessionId }),
    }, provider)
    const span = scoped.span ?? {
      request: scoped.request === undefined ? undefined : resolveMessageForRendering(scoped.request, provider),
      result: scoped.result === undefined ? undefined : resolveMessageForRendering(scoped.result, provider),
      role: scoped.role ?? 'result',
      visibleRows: { request: scoped.request !== undefined, result: scoped.result !== undefined || (scoped.role ?? 'result') === 'result' },
    }
    const extraction: RowExtractionInput = {
      ...scoped,
      resolved,
      category: scoped.category ?? plugin.transcript.classify(resolved),
      span,
    }
    return hook(extraction, call)
  }
}
