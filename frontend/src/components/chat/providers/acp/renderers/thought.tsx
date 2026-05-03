import type { JSX } from 'solid-js'
import type { RenderContext } from '../../../messageRenderers'
import { ThinkingMessage } from '../../../messageRenderers'
import { extractAgentText } from './helpers'

/** Render an ACP agent_thought_chunk as collapsible thinking (same style as Claude Code). */
export function acpThoughtRenderer(parsed: unknown, context?: RenderContext): JSX.Element | null {
  const text = extractAgentText(parsed)
  if (!text)
    return null
  return <ThinkingMessage text={text} context={context} />
}
