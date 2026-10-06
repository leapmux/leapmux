import type { Locator, Page } from '@playwright/test'
import type { MockModelRequestRecord, MockModelToolCall } from './mockModelScript'
import type { NativeMessageSnapshot } from './nativeMessages'
import type { ManagedNativeScenarioContext, NativeScenarioContext } from './nativeScenario'
import type { GatedOutput } from './outputGate'
import { Buffer } from 'node:buffer'
import { randomUUID } from 'node:crypto'
import { existsSync, readFileSync, writeFileSync } from 'node:fs'
import { basename, join } from 'node:path'
import process from 'node:process'
import { expect } from '@playwright/test'
import { expectNoNativeControl } from './nativeControlObservation'
import { readNativeMessageSnapshot } from './nativeMessages'
import { currentNativeAgent, nativeTextStep, nativeToolOutcome } from './nativeScenario'
import { waitForNativeToolSteps } from './nativeToolExecution'
import { runWithGatedOutput } from './outputGate'
import { bashToolCall } from './providerToolCalls'
import { quotePosixShellArgument } from './shellArguments'
import { answerControl, assistantBubbles, openWorkspace, sendMessage, toolCallRow, waitForAgentIdle, waitForControlBanner } from './ui'

/** A real native operation retains its file guard and the exact result proof. */
export interface NativePermissionOperationPlan {
  toolCall: MockModelToolCall
  /**
   * The gate that holds the command of `toolCall` until the browser shows its output.
   * A provider whose shell tool can lose the output of a fast command sets it (see `OutputGate`).
   */
  outputGate?: GatedOutput
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
    /**
     * The gate that holds the command of `toolCall`. The gate opens after the browser shows the
     * output of the allowed command. A denied command prints nothing, so a denial cannot use it.
     */
    outputGate?: GatedOutput
    beforeDecision?: (banner: Locator) => void | Promise<void>
    nativeProof: (request: MockModelRequestRecord) => void | Promise<void>
  },
): Promise<void> {
  if (options.outputGate && options.decision === 'deny')
    throw new Error('A denied command prints no output, so it cannot open an output gate.')
  const answer = 'The native permission decision reached the next turn.'
  const start = await context.modelScript.queue({ toolCalls: [options.toolCall] }, nativeTextStep(context, answer))
  await sendMessage(context.page, context.modelScript.prompt('Run the scripted permission probe.'))
  await context.modelScript.waitForSteps(start + 1)
  const banner = await waitForControlBanner(context.page)
  await options.beforeDecision?.(banner)
  await expect(context.page.locator('[data-testid="dialog-editor"]:visible')).toHaveCount(0)
  await answerControl(context.page, options.decision)
  await runWithGatedOutput(options.outputGate, async () => {
    await context.modelScript.waitForSteps(start + 2)
    await waitForAgentIdle(context.page)
  })
  await options.nativeProof(await context.modelScript.requestAt(start + 1))
  await expect(assistantBubbles(context.page).filter({ hasText: answer }).first()).toBeVisible()
}

/**
 * Require the visible result row of one tool call to state a declined call.
 * The row carries the declined status and its heading, and it keeps the native refusal text when the caller gives one.
 */
export async function expectDeclinedToolRow(page: Page, renderedCallId: string, refusal?: string): Promise<void> {
  const row = toolCallRow(page, renderedCallId)
  await expect(row).toHaveCount(1)
  await expect(row).toHaveAttribute('data-tool-status', 'declined')
  await expect(row).toContainText('Declined')
  if (refusal !== undefined)
    await expect(row).toContainText(refusal)
}

/**
 * Deny an actual native permission request whose runtime ends the turn after the refusal.
 * Such a runtime sends no further model request, so the scenario queues only the turn that asks for the tool.
 * A model turn queued after it would stay unconsumed.
 * After the decision and after a reload, the scenario proves the unchanged target, the same native session,
 * the stored native refusal, and that the model received no further request.
 */
export async function exerciseNativePermissionRefusal(context: ManagedNativeScenarioContext, options: {
  toolCall: MockModelToolCall
  prompt: string
  /** Text that identifies the operation in the native permission banner. */
  bannerText?: string
  /** Prove the unchanged target. The scenario calls it before the decision, after it, and after a reload. */
  expectUnchanged: () => void
  /** Prove the stored native refusal from a Worker snapshot of the same agent. Throw when the snapshot does not prove it. */
  nativeRefusal: (snapshot: NativeMessageSnapshot) => void
  /** Prove provider-owned view state after the decision and after a reload. */
  viewProof?: () => Promise<void>
}): Promise<void> {
  const agent = await currentNativeAgent(context)
  const start = await context.modelScript.queue({ toolCalls: [options.toolCall] })
  await sendMessage(context.page, context.modelScript.prompt(options.prompt))
  await context.modelScript.waitForSteps(start + 1)
  const banner = await waitForControlBanner(context.page)
  if (options.bannerText !== undefined)
    await expect(banner).toContainText(options.bannerText)
  options.expectUnchanged()
  await expect(context.page.locator('[data-testid="dialog-editor"]:visible')).toHaveCount(0)
  await answerControl(context.page, 'deny')
  await waitForAgentIdle(context.page)
  await expect(banner).toHaveCount(0)
  for (const reload of [false, true]) {
    if (reload) {
      await context.page.reload()
      await openWorkspace(context.page, context.workspaceId)
    }
    options.expectUnchanged()
    const current = await currentNativeAgent(context)
    expect(current.id).toBe(agent.id)
    expect(current.agentSessionId).toBe(agent.agentSessionId)
    // Read until the Worker holds the refusal row. The poll reports the last reader error when it never does.
    await expect.poll(async () => {
      const snapshot = await readNativeMessageSnapshot(context, agent.id)
      try {
        options.nativeRefusal(snapshot)
        return 'proved'
      }
      catch (error) {
        return error instanceof Error ? error.message : String(error)
      }
    }).toBe('proved')
    await options.viewProof?.()
    const status = await context.modelScript.status()
    expect(status.stepCount).toBe(start + 1)
    expect(status.nextStep).toBe(start + 1)
    expect(status.unexpectedRequests).toEqual([])
  }
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
    const start = await context.modelScript.queue({ toolCalls: [scenario.toolCall] }, nativeTextStep(context, 'The native preset write ended.'))
    await sendMessage(context.page, context.modelScript.prompt('Run the scripted native preset write.'))
    if (options.permission === 'native')
      await waitForNativeToolSteps(context, start + 2)
    else
      await context.modelScript.waitForSteps(start + 2)
    await waitForAgentIdle(context.page)
    await scenario.prove(await context.modelScript.requestAt(start + 1))
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
      expect((await nativeToolOutcome(context, request, callId)).text).toContain(output)
    },
  }
}
