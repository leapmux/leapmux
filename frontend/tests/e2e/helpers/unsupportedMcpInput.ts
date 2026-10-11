import type { MockModelRequestRecord } from './mockModelScript'
import type { ManagedNativeScenarioContext } from './nativeScenario'
import { expect } from '@playwright/test'
import { nativeMcpCancellation, nativeMcpRefusal, nativeMcpUnansweredInput, readMcpServerReceipt } from './mcpServerReceipt'
import { expectNoNativeControl } from './nativeControlObservation'
import { nativeToolOutcome } from './nativeScenario'

interface McpInputProbe {
  receiptLog: string
  callId: string
  invoke: () => Promise<MockModelRequestRecord>
  /**
   * More controls that must not appear during the call, such as `control-banner` under a preset that runs every tool
   * without a request. The form of the probe is always watched.
   */
  additionalTestIds?: readonly string[]
}

/** Read the native tool result of the probe call in the model request that follows it. */
async function probeToolResult(context: ManagedNativeScenarioContext, request: MockModelRequestRecord, callId: string): Promise<string> {
  return (await nativeToolOutcome(context, request, callId)).text
}

/** Prove the actual native client refuses a form and returns that refusal to its model. */
export async function expectUnsupportedMcpInput(context: ManagedNativeScenarioContext, options: McpInputProbe): Promise<void> {
  await expectNoNativeControl(context, {
    testId: 'elicitation-form',
    additionalTestIds: options.additionalTestIds ?? [],
    relatedProof: async () => {
      const request = await options.invoke()
      const refusal = nativeMcpRefusal(readMcpServerReceipt(options.receiptLog))
      expect(await probeToolResult(context, request, options.callId)).toContain(refusal.toolResult.text)
    },
  })
}

/** Prove the actual native client cancels a form that it cannot show, and returns that cancel to its model. */
export async function expectCancelledMcpInput(context: ManagedNativeScenarioContext, options: McpInputProbe): Promise<void> {
  await expectNoNativeControl(context, {
    testId: 'elicitation-form',
    additionalTestIds: options.additionalTestIds ?? [],
    relatedProof: async () => {
      const request = await options.invoke()
      const cancellation = nativeMcpCancellation(readMcpServerReceipt(options.receiptLog))
      expect(await probeToolResult(context, request, options.callId)).toContain(cancellation.toolResult.text)
    },
  })
}

/**
 * Prove the actual native client leaves a form unanswered and returns its own failure to its model.
 *
 * For a client that sends no reply at all, neither a refusal nor a decision. Its tool call then ends
 * with the client's own error, and the tool result that the model reads must contain
 * `nativeFailureText`.
 */
export async function expectUnansweredMcpInput(context: ManagedNativeScenarioContext, options: McpInputProbe & { nativeFailureText: string }): Promise<void> {
  if (!options.nativeFailureText)
    throw new Error('The unanswered MCP input proof requires the exact native failure text.')
  await expectNoNativeControl(context, {
    testId: 'elicitation-form',
    additionalTestIds: options.additionalTestIds ?? [],
    relatedProof: async () => {
      const request = await options.invoke()
      nativeMcpUnansweredInput(readMcpServerReceipt(options.receiptLog))
      expect(await probeToolResult(context, request, options.callId)).toContain(options.nativeFailureText)
    },
  })
}
