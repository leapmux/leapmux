import type { JSX } from 'solid-js'
import type { RenderContext } from '../../../messageRenderers'
import type { MessageRole } from '~/generated/leapmux/v1/agent_pb'
import { ThinkingMessage } from '../../../messageRenderers'
import { extractAgentText } from './helpers'

/** Render an ACP agent_thought_chunk as collapsible thinking (same style as Claude Code). */
export function acpThoughtRenderer(parsed: unknown, _role: MessageRole, context?: RenderContext): JSX.Element | null {
  const text = extractAgentText(parsed)
  if (!text)
    return null
  return <ThinkingMessage text={text} context={context} />
}
