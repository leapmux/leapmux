import { CODEWHALE_BLOCK_FIELD, CODEWHALE_BLOCK_TYPE, CODEWHALE_ITEM_KIND, CODEWHALE_TRANSCRIPT_ROLE } from '~/generated/contracts/codewhale-protocol'
import { pickString } from '~/lib/jsonPick'
import { codewhaleChildBlock, codewhaleItem } from './toolCommon'

/**
 * The words one message row states, and whether they are the reply or the reasoning.
 *
 * The main transcript states a message as the FINAL event of an `agent_message` or an
 * `agent_reasoning` item, whose `detail` holds the whole text. A subagent's transcript
 * states it as a `text` or a `thinking` block of an assistant message. Null for a row
 * that is neither.
 *
 * The text is returned as the runtime wrote it. A row whose text is empty is a message
 * with nothing to show, and the caller decides what that draws.
 */
export function codewhaleMessageText(parsed: unknown): { kind: 'text' | 'thinking', text: string } | null {
  const item = codewhaleItem(parsed)
  if (item) {
    if (item.outcome === 'open')
      return null
    if (item.kind === CODEWHALE_ITEM_KIND.AgentMessage)
      return { kind: 'text', text: item.detail }
    if (item.kind === CODEWHALE_ITEM_KIND.AgentReasoning)
      return { kind: 'thinking', text: item.detail }
    return null
  }
  const child = codewhaleChildBlock(parsed)
  if (!child || child.role !== CODEWHALE_TRANSCRIPT_ROLE.Assistant)
    return null
  if (child.type === CODEWHALE_BLOCK_TYPE.Text)
    return { kind: 'text', text: pickString(child.block, CODEWHALE_BLOCK_FIELD.Text) }
  if (child.type === CODEWHALE_BLOCK_TYPE.Thinking)
    return { kind: 'thinking', text: pickString(child.block, CODEWHALE_BLOCK_FIELD.Thinking) }
  return null
}
