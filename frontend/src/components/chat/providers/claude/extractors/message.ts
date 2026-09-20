import type { ToolCallSpecVariant } from '../../../model/toolCall'
import type { MessageRequest } from '../../../model/tools/message'
import type { ClaudeToolRow } from './toolCommon'
import { proseResult } from '../../../model/toolCall'
import { claudeToolFailureResult } from './failure'

/**
 * The message pair: who it went to and what it said. `summary` is the model's
 * own one-line preview of the message, written for exactly this slot.
 *
 * A send that FAILED states its reason alone. It drew as the delivery receipt
 * otherwise, under the same header as a message that arrived.
 */
export function claudeMessageSpec(request: MessageRequest, args: ClaudeToolRow, result: ClaudeToolRow | undefined): ToolCallSpecVariant<'message'> {
  // With no addressee the tool's own name words the header; with one, the header
  // key stays absent rather than present-and-undefined.
  const header = request.to ? {} : { title: args.toolName }
  if (!result)
    return { kind: 'message', request, ...header }
  const failure = claudeToolFailureResult(result)
  if (failure)
    return { kind: 'message', request, ...header, result: failure }
  return { kind: 'message', request, ...header, result: proseResult(result.resultContent) }
}
