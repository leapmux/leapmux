import { nativeModelToolNames } from '../helpers/nativeScenario'

/**
 * The tool names that one recorded model call of Cline OFFERED, read from its `tools`.
 *
 * Cline calls the model through its `deepseek` provider, which speaks the OpenAI
 * Chat Completions protocol. A body without a nonempty catalog fails, so a check
 * that a tool is absent cannot pass on a request that offered no tool at all.
 *
 * A tool name can also appear in the conversation that the call carries, as an
 * earlier call of the tool, so a search of the whole body cannot tell what the
 * session offers now.
 */
export function offeredTools(body: unknown): string[] {
  return nativeModelToolNames({ protocol: 'openai-chat-completions', body })
}
