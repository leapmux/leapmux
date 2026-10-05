import type { MockModelRequestRecord } from './mockModelScript'
import type { ManagedNativeScenarioContext } from './nativeScenario'
import { writeFileSync } from 'node:fs'
import { join } from 'node:path'
import { expect } from '@playwright/test'
import { currentNativeAgent } from './nativeScenario'
import { waitForNativeToolSteps } from './nativeToolExecution'
import { nativeToolResult } from './nativeToolResult'
import { readToolCall } from './providerToolCalls'
import { sendMessage } from './ui'

/** Complete a native read-only plan from actual file context and inspect its next model request. */
export async function exerciseNativeReadOnlyPlan(
  context: ManagedNativeScenarioContext,
  options: {
    preparePlan: () => Promise<void>
    nativeProof: (request: MockModelRequestRecord) => void | Promise<void>
  },
): Promise<void> {
  await options.preparePlan()
  const agent = await currentNativeAgent(context)
  if (!agent.workingDir)
    throw new Error('The native plan proof requires a private working directory.')
  const file = join(agent.workingDir, 'native-read-only-plan-context.txt')
  const marker = 'NATIVE_READ_ONLY_PLAN_CONTEXT'
  writeFileSync(file, `${marker}\n`)
  const callId = 'native-read-only-plan'
  const start = await context.modelScript.queue(
    { toolCalls: [readToolCall(context.provider, callId, file)] },
    { text: '# Native plan\n\n1. Inspect the file context.\n2. Implement after the user selects execution mode.' },
  )
  await sendMessage(context.page, context.modelScript.prompt('Read the supplied context and return the read-only plan.'))
  await waitForNativeToolSteps(context, start + 2)
  const request = await context.modelScript.requestAt(start + 1)
  expect(nativeToolResult(request, callId)).toContain(marker)
  await options.nativeProof(request)
}
