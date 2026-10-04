import type { Locator } from '@playwright/test'
import type { MockModelRequestRecord, MockModelToolCall } from './mockModelScript'
import type { ManagedNativeScenarioContext, NativeScenarioContext } from './nativeScenario'
import { Buffer } from 'node:buffer'
import { randomUUID } from 'node:crypto'
import { existsSync, readFileSync, writeFileSync } from 'node:fs'
import { basename, join } from 'node:path'
import process from 'node:process'
import { expect } from '@playwright/test'
import { expectNoNativeControl } from './nativeControlObservation'
import { currentNativeAgent, nativeTextStep } from './nativeScenario'
import { waitForNativeToolSteps } from './nativeToolExecution'
import { nativeToolResult } from './nativeToolResult'
import { bashToolCall } from './providerToolCalls'
import { quotePosixShellArgument } from './shellArguments'
import { assistantBubbles, sendMessage, waitForAgentIdle, waitForControlBanner } from './ui'

/** A real native operation retains its file guard and the exact result proof. */
export interface NativePermissionOperationPlan {
  toolCall: MockModelToolCall
  beforeDecision: () => void | Promise<void>
  nativeProof: (request: MockModelRequestRecord) => void | Promise<void>
}

interface NativePermissionFileOptions {
  fileName: string
  callId: string
  outputPrefix: string
  initialContent?: string
}

/** Prepare a real file creation without replacing its absent-file guard with a seeded file. */
export async function createNativePermissionFileWrite(context: ManagedNativeScenarioContext, options: NativePermissionFileOptions): Promise<NativePermissionOperationPlan> {
  if (!options.fileName || options.fileName === '.' || options.fileName === '..' || basename(options.fileName) !== options.fileName || options.fileName.includes('\\') || options.fileName.includes('\0'))
    throw new Error('The native permission file requires one filename component.')
  if (!options.callId || !options.outputPrefix)
    throw new Error('The native permission file requires a call ID and output prefix.')
  const scenario = await nativeWriteScenario(context, options)
  return {
    toolCall: scenario.toolCall,
    beforeDecision: () => {
      if (options.initialContent === undefined)
        expect(existsSync(scenario.file)).toBe(false)
      else
        expect(readFileSync(scenario.file, 'utf8')).toBe(options.initialContent)
    },
    nativeProof: scenario.prove,
  }
}

/** Answer an actual native permission request and prove its subsequent result. */
export async function exerciseNativePermissionDecision(
  context: NativeScenarioContext,
  options: {
    toolCall: MockModelToolCall
    decision: 'allow' | 'deny'
    beforeDecision?: (banner: Locator) => void | Promise<void>
    nativeProof: (request: MockModelRequestRecord) => void | Promise<void>
  },
): Promise<void> {
  const start = (await context.modelScript.status()).stepCount
  const answer = 'The native permission decision reached the next turn.'
  await context.modelScript.queue({ toolCalls: [options.toolCall] }, nativeTextStep(context, answer))
  await sendMessage(context.page, context.modelScript.prompt('Run the scripted permission probe.'))
  await context.modelScript.waitForSteps(start + 1)
  const banner = await waitForControlBanner(context.page)
  await options.beforeDecision?.(banner)
  await expect(context.page.locator('[data-testid="dialog-editor"]:visible')).toHaveCount(0)
  await context.page.locator(`[data-testid="control-${options.decision}-btn"]:visible`).first().click()
  const status = await context.modelScript.waitForSteps(start + 2)
  await waitForAgentIdle(context.page)
  const request = status.requests.find(record => record.stepIndex === start + 1)
  if (!request)
    throw new Error('The native permission decision produced no next model request.')
  await options.nativeProof(request)
  await expect(assistantBubbles(context.page).filter({ hasText: answer }).first()).toBeVisible()
}

/** Require native Allow before a real file change and calculated command output. */
export async function exerciseNativePermissionWrite(
  context: ManagedNativeScenarioContext,
  options: { prepare?: () => Promise<void> } = {},
): Promise<void> {
  await options.prepare?.()
  const scenario = await nativeWriteScenario(context)
  await exerciseNativePermissionDecision(context, {
    toolCall: scenario.toolCall,
    decision: 'allow',
    beforeDecision: () => {
      expect(readFileSync(scenario.file, 'utf8')).toBe(scenario.before)
    },
    nativeProof: scenario.prove,
  })
}

/** Run an actual write under the provider's current preset and verify its native result. */
export async function exerciseNativeToolWrite(
  context: ManagedNativeScenarioContext,
  options: { permission: 'native' | 'absent', prepare?: () => Promise<void> },
): Promise<void> {
  await options.prepare?.()
  const scenario = await nativeWriteScenario(context)
  const run = async () => {
    const start = (await context.modelScript.status()).stepCount
    await context.modelScript.queue({ toolCalls: [scenario.toolCall] }, nativeTextStep(context, 'The native preset write ended.'))
    await sendMessage(context.page, context.modelScript.prompt('Run the scripted native preset write.'))
    if (options.permission === 'native')
      await waitForNativeToolSteps(context, start + 2)
    else
      await context.modelScript.waitForSteps(start + 2)
    await waitForAgentIdle(context.page)
    const request = (await context.modelScript.status()).requests.find(record => record.stepIndex === start + 1)
    if (!request)
      throw new Error('The native preset write produced no next model request.')
    await scenario.prove(request)
    await expect(assistantBubbles(context.page).filter({ hasText: 'The native preset write ended.' }).first()).toBeVisible()
  }
  if (options.permission === 'absent')
    await expectNoNativeControl(context, { testId: 'control-banner', relatedControl: run })
  else
    await run()
}

/** Prepare calculated output and private file bytes that only native execution can produce. */
async function nativeWriteScenario(context: ManagedNativeScenarioContext, options?: NativePermissionFileOptions) {
  const agent = await currentNativeAgent(context)
  if (!agent.workingDir)
    throw new Error('The native permission write requires a working directory.')
  const marker = randomUUID().replaceAll('-', '')
  const file = join(agent.workingDir, options?.fileName ?? `native-permission-${marker}.txt`)
  const before = options?.initialContent ?? `BEFORE${marker}`
  const afterPrefix = options?.outputPrefix ?? `AFTER${marker}`
  const outputPrefix = options?.outputPrefix ?? `NATIVEPERMISSION${marker}`
  const ending = options ? '\n' : ''
  const after = `${afterPrefix}42${ending}`
  const output = `${outputPrefix}42`
  if (!options || options.initialContent !== undefined)
    writeFileSync(file, before)
  else
    expect(existsSync(file)).toBe(false)
  const source = `require('node:fs').writeFileSync(${JSON.stringify(file)},${JSON.stringify(afterPrefix)}+(40+2)+${JSON.stringify(ending)});process.stdout.write(${JSON.stringify(outputPrefix)}+(40+2)+${JSON.stringify(ending)})`
  const code = `eval(Buffer.from(${JSON.stringify(Buffer.from(source).toString('base64'))},'base64').toString())`
  const command = `${quotePosixShellArgument(process.execPath)} -e ${quotePosixShellArgument(code)}`
  const callId = options?.callId ?? `native-permission-${marker}`
  return {
    toolCall: bashToolCall(context.provider, callId, command),
    file,
    before,
    prove: async (request: MockModelRequestRecord) => {
      expect(readFileSync(file, 'utf8')).toBe(after)
      const result = context.readToolResult ? await context.readToolResult(request, callId) : { text: nativeToolResult(request, callId) }
      expect(result.text).toContain(output)
    },
  }
}
