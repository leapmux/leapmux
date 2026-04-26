/* eslint-disable solid/no-innerhtml -- HTML is produced via remark, not arbitrary user input */
import type { JSX } from 'solid-js'
import { renderMarkdown } from '~/lib/renderMarkdown'
import { markdownContent } from '../../../markdownEditor/markdownContent.css'
import { extractAgentText } from './helpers'

/** Render an ACP agent_message_chunk as markdown. */
export function acpAgentMessageRenderer(parsed: unknown): JSX.Element | null {
  const text = extractAgentText(parsed)
  if (!text)
    return null
  return <div class={markdownContent} innerHTML={renderMarkdown(text)} />
}
