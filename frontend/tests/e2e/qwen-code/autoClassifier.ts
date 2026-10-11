import type { MockModelRule, MockModelToolCall } from '../helpers/mockModelScript'
import type { NativeMessageSnapshot } from '../helpers/nativeMessages'
import { expect } from '@playwright/test'
import { acpClosedToolCall } from '../helpers/acpToolFrame'
import { readNativeToolOutputRecord } from '../helpers/nativeMessages'

/** The system prompt of the classifier request of Qwen's Auto mode starts with this text. */
const QWEN_CLASSIFIER_SYSTEM = '^You are a security classifier'

/**
 * A rule that answers the classifier of Qwen's Auto mode without a verdict.
 *
 * Auto mode can ask a classifier model before it runs a tool call, as it does for a write to `package.json`. The
 * classifier request has its own system prompt, and it is not a turn of the conversation, so a rule answers it, and
 * the scripted turns keep their order. An ordered step would answer the classifier in place of the turn that the test
 * scripted for it.
 *
 * An answer that states no verdict makes Qwen ask the reader ("Auto Mode couldn't classify this action"). A reject then
 * ends the turn: Qwen sends the model no tool result and no further request.
 */
export function qwenClassifierWithoutVerdict(name: string): MockModelRule {
  if (!name.trim())
    throw new Error('The Qwen classifier rule requires a name.')
  return { name, when: { system: QWEN_CLASSIFIER_SYSTEM }, respond: { text: 'The classifier states no verdict.' } }
}

/** Require the refusal that Qwen stores for `toolCall` after the reader rejects it in a Worker snapshot of the agent. */
export function expectQwenCanceledTool(snapshot: NativeMessageSnapshot, toolCall: MockModelToolCall): void {
  const refusal = readNativeToolOutputRecord(snapshot, {
    callId: toolCall.id,
    spanId: toolCall.id,
    accepts: frame => acpClosedToolCall(frame, toolCall.id, ['failed']),
  })
  expect(refusal.frame.content).toEqual([{ type: 'content', content: { type: 'text', text: `Tool "${toolCall.name}" was canceled by the user.` } }])
}
