/**
 * Client frames that the Cursor unit tests send to the mock, built from the
 * shared codec in `./cursorProtobuf`. The real CLI writes these frames; a test
 * writes the same field numbers to drive the surface without the CLI.
 */
import { concatBytes, encodeLengthDelimited, encodeStringField, encodeVarintField } from './cursorProtobuf'

/** The `AgentClientMessage` that OPENS a turn, which carries the user's prompt. */
export function clientMessageWithPrompt(text: string): Uint8Array {
  return encodeLengthDelimited(1, // AgentClientMessage.run_request
    encodeLengthDelimited(2, // AgentRunRequest.action
      encodeLengthDelimited(1, // ConversationAction.user_message_action
        encodeLengthDelimited(1, // UserMessageAction.user_message
          encodeStringField(1, text), // UserMessage.text
        ))))
}

/**
 * The CLI's answer to request context query `id`, with the rules that it states:
 * `ExecClientMessage.request_context_result` (field 10), whose success holds a
 * `RequestContext` with one `CursorRule` for each rule.
 */
export function cursorRequestContextReply(id: number, rules: readonly { path: string, content: string }[]): Uint8Array {
  const context = concatBytes(rules.map(rule => encodeLengthDelimited(2, concatBytes([encodeStringField(1, rule.path), encodeStringField(2, rule.content)]))))
  return encodeLengthDelimited(2, concatBytes([
    encodeVarintField(1, id),
    encodeLengthDelimited(10, encodeLengthDelimited(1, encodeLengthDelimited(1, context))),
  ]))
}

/**
 * The `ExecClientMessage` that answers execution `id`: the result message at
 * `field`, which holds one outcome message at `outcome`.
 */
export function executionReplyFrame(id: number, field: number, outcome: number, payload: Uint8Array, execID = 'call'): Uint8Array {
  return encodeLengthDelimited(2, concatBytes([
    encodeVarintField(1, id),
    encodeStringField(15, execID),
    encodeLengthDelimited(field, encodeLengthDelimited(outcome, payload)),
  ]))
}

/** The client's reply to a native subagent execution, with its result message. */
export function cursorSubagentReplyFixture(id: number, outcome: Uint8Array, execID = 'native-task'): Uint8Array {
  return encodeLengthDelimited(2, concatBytes([
    encodeVarintField(1, id),
    encodeStringField(15, execID),
    encodeLengthDelimited(28, outcome),
  ]))
}

/** The success result of a native subagent, with the fields that the options state. */
export function cursorSubagentSuccessFixture(options: { agentID?: string, finalMessage?: string, toolCallCount?: number } = {}): Uint8Array {
  return encodeLengthDelimited(1, concatBytes([
    ...(options.agentID !== undefined ? [encodeStringField(1, options.agentID)] : []),
    ...(options.finalMessage !== undefined ? [encodeStringField(2, options.finalMessage)] : []),
    ...(options.toolCallCount !== undefined ? [encodeVarintField(3, options.toolCallCount)] : []),
  ]))
}
