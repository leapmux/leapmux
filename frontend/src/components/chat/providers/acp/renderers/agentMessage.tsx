import type { JSX } from 'solid-js'
import { MarkdownText } from '../../../messageRenderers'
import { extractAgentText } from './helpers'

/** Render an ACP agent_message_chunk as markdown. */
export function acpAgentMessageRenderer(parsed: unknown): JSX.Element | null {
  const text = extractAgentText(parsed)
  if (!text)
    return null
  return <MarkdownText text={text} />
}
