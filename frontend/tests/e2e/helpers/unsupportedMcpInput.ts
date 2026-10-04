import type { MockModelRequestRecord } from './mockModelScript'
import type { ManagedNativeScenarioContext } from './nativeScenario'
import { expect } from '@playwright/test'
import { nativeMcpRefusal, readMcpServerReceipt } from './mcpServerReceipt'
import { expectNoNativeControl } from './nativeControlObservation'
import { nativeToolResult } from './nativeToolResult'

/** Prove the actual native client refuses a form and returns that refusal to its model. */
export async function expectUnsupportedMcpInput(
  context: ManagedNativeScenarioContext,
  options: {
    receiptLog: string
    callId: string
    invoke: () => Promise<MockModelRequestRecord>
  },
): Promise<void> {
  await expectNoNativeControl(context, {
    testId: 'elicitation-form',
    relatedControl: async () => {
      const request = await options.invoke()
      const refusal = nativeMcpRefusal(readMcpServerReceipt(options.receiptLog))
      const result = context.readToolResult
        ? await context.readToolResult(request, options.callId)
        : { text: nativeToolResult(request, options.callId) }
      expect(result.text).toContain(refusal.toolResult.text)
    },
  })
}
