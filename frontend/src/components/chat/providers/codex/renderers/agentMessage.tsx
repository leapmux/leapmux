/* eslint-disable solid/no-innerhtml -- HTML is produced via remark, not arbitrary user input */
import type { JSX } from 'solid-js'
import type { RenderContext } from '../../../messageRenderers'
import type { MessageRole } from '~/generated/leapmux/v1/agent_pb'
import { renderMarkdown } from '~/lib/renderMarkdown'
import { markdownContent } from '../../../markdownEditor/markdownContent.css'
import { extractItem } from '../renderHelpers'

/** Renders Codex agentMessage items as markdown. */
export function codexAgentMessageRenderer(parsed: unknown, _role: MessageRole, _context?: RenderContext): JSX.Element | null {
  const item = extractItem(parsed)
  if (!item || item.type !== 'agentMessage')
    return null
  const text = (item.text as string) || ''
  if (!text)
    return null
  return <div class={markdownContent} innerHTML={renderMarkdown(text)} />
}
