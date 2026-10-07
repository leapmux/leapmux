import type { Locator, Page } from '@playwright/test'
import type { MockModelRequestRecord, MockModelStep, MockModelToolCall } from './mockModelScript'
import type { NativeMessageSnapshot } from './nativeMessages'
import type { ManagedNativeScenarioContext, NativeAnswerStep, NativeScenarioContext } from './nativeScenario'
import type { GatedOutput } from './outputGate'
import { Buffer } from 'node:buffer'
import { existsSync, readFileSync, rmSync, writeFileSync } from 'node:fs'
import { isAbsolute, join } from 'node:path'
import process from 'node:process'
import { expect } from '@playwright/test'
import { CONTROL_RESPONSE_FEEDBACK_LEAD } from '../../../src/components/chat/persistedControlResponse'
import { withCleanup } from './cleanup'
import { expectTurnEndedAfter } from './modelScriptFixture'
import { expectNoNativeControl } from './nativeControlObservation'
import { readNativeMessageSnapshot } from './nativeMessages'
import { currentNativeAgent, nativeScenarioModelContextText, nativeTextStep, nativeToolOutcome, toolTurnSteps } from './nativeScenario'
import { runNativeToolTurn } from './nativeToolExecution'
import { runWithGatedOutput } from './outputGate'
import { bashToolCall } from './providerToolCalls'
import { retryUntilPass } from './retryUntilPass'
import { isFileNameComponent } from './runDirectory'
import { quotePosixShellArgument, uniqueMarker } from './shellArguments'
import { answerControl, assistantBubbles, controlActions, controlButton, enterControlFeedback, expectNoControlBanner, messageBubbles, openWorkspace, PLATFORM_MOD, sendMessage, toolCallRow, userBubbles, waitForAgentIdle, waitForControlBanner } from './ui'

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
  if (!isFileNameComponent(options.fileName))
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

/** What {@link exerciseNativePermissionDecision} runs around one native permission request. */
export interface NativePermissionDecision {
  toolCall: MockModelToolCall
  decision: 'allow' | 'deny'
  /**
   * The gate that holds the command of `toolCall`. The gate opens after the browser shows the
   * output of the allowed command. A denied command prints nothing, so a denial cannot use it.
   */
  outputGate?: GatedOutput
  /** Check the banner of the request, and the target of the tool before the decision. */
  beforeDecision?: (banner: Locator) => void | Promise<void>
  /** Prove the native result of the decision from the model request that follows it. */
  nativeProof: (request: MockModelRequestRecord) => void | Promise<void>
  /** Prove provider-owned view state after the turn ended, such as the saved answer of the request. */
  viewProof?: () => Promise<void>
}

/**
 * Answer an actual native permission request, prove its subsequent result, and return the model request that
 * follows the decision. The turn must continue after the decision: the runtime sends that request with the tool
 * result or the refusal. A runtime that ends the turn after a refusal needs `exerciseNativePermissionRefusal`.
 */
export async function exerciseNativePermissionDecision(context: NativeScenarioContext, options: NativePermissionDecision): Promise<MockModelRequestRecord> {
  if (options.outputGate && options.decision === 'deny')
    throw new Error('A denied command prints no output, so it cannot open an output gate.')
  // A test can answer two requests in one session, so each decision gets its own answer text.
  const answer = `The native permission decision reached the next turn. ${uniqueMarker('DECISION')}`
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
  const request = await context.modelScript.requestAt(start + 1)
  await options.nativeProof(request)
  await expect(assistantBubbles(context.page).filter({ hasText: answer }).first()).toBeVisible()
  await options.viewProof?.()
  return request
}

/**
 * Return a proof that allows one real native operation through its permission banner, for a check that observes the
 * banner while the operation runs.
 *
 * Before the decision, the guard of the operation reads the target first, so nothing can change it before the read.
 * The check of the caller reads the banner next. After the decision, the native proof of the operation runs, then
 * `nativeProof`. An output gate of the operation goes with its tool call.
 */
export function allowNativeOperation(
  context: NativeScenarioContext,
  operation: NativePermissionOperationPlan,
  nativeProof?: (request: MockModelRequestRecord) => void | Promise<void>,
): (checkBanner: (banner: Locator) => Promise<void>) => Promise<MockModelRequestRecord> {
  return checkBanner => exerciseNativePermissionDecision(context, {
    toolCall: operation.toolCall,
    ...(operation.outputGate ? { outputGate: operation.outputGate } : {}),
    decision: 'allow',
    beforeDecision: async (banner) => {
      await operation.beforeDecision()
      await checkBanner(banner)
    },
    nativeProof: async (request) => {
      await operation.nativeProof(request)
      await nativeProof?.(request)
    },
  })
}

/**
 * Allow one shell command that writes a file, then refuse the next one with a typed reason that the same turn reads.
 * The runtime puts the reason into its native refusal, hands it to the model inside the same turn, and the model
 * answers. The refused command never runs, and the saved answer shows the reason, not the words of an option.
 * Grok Build and Kiro carry a reason this way.
 */
export async function exerciseAllowThenFeedbackRejection(context: NativeScenarioContext, options: { workingDir: string }): Promise<void> {
  // The commands hold the paths unquoted, as the model writes them, so a path must not need shell quoting.
  if (!isAbsolute(options.workingDir) || !/^[\w./-]+$/.test(options.workingDir))
    throw new Error('The allow-then-refuse scenario requires an absolute working directory that needs no shell quoting.')
  const approved = join(options.workingDir, 'approved.txt')
  const rejected = join(options.workingDir, 'rejected.txt')
  const reason = 'Do not create the second file.'
  const answer = 'I read the reason and stopped.'
  // Each command redirects its output, because a write is what makes these runtimes ask. The banner shows the command as
  // the model wrote it.
  const approvedCommand = `printf approved > ${approved}`
  const rejectedCommand = `printf rejected > ${rejected}`
  const start = await context.modelScript.queue(
    { toolCalls: [bashToolCall(context.provider, 'native-approved', approvedCommand)] },
    { toolCalls: [bashToolCall(context.provider, 'native-rejected', rejectedCommand)] },
    nativeTextStep(context, answer),
  )
  await sendMessage(context.page, context.modelScript.prompt('Create the two scripted files.'))
  await context.modelScript.waitForSteps(start + 1)
  const banner = await waitForControlBanner(context.page)
  await expect(banner).toContainText(approvedCommand)
  expect(existsSync(approved)).toBe(false)
  await answerControl(context.page, 'allow')

  await context.modelScript.waitForSteps(start + 2)
  await expect(banner).toContainText(rejectedCommand)
  expect(existsSync(approved)).toBe(true)
  // Text in the composer turns the decision into a refusal with that text, and the send key answers the request.
  await enterControlFeedback(context.page, reason)
  await context.page.keyboard.press(`${PLATFORM_MOD}+Enter`)
  await expect(banner).toHaveCount(0)
  await context.modelScript.waitForSteps(start + 3)
  await waitForAgentIdle(context.page)

  // The reason reached the model inside the same turn, and the saved answer shows it.
  expect(JSON.stringify((await context.modelScript.requestAt(start + 2)).body)).toContain(reason)
  await expectSavedRefusalFeedback(context.page, reason)
  await expect(assistantBubbles(context.page).filter({ hasText: answer })).toBeVisible()
  expect(existsSync(rejected)).toBe(false)
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
 * Require the result row of one refused call to state a declined call, then reload the page and require it again.
 * The Worker stores the refusal, so a reload must draw the same declined row.
 */
export async function expectDeclinedToolRowAcrossReload(
  context: Pick<ManagedNativeScenarioContext, 'page' | 'workspaceId'>,
  renderedCallId: string,
  refusal?: string,
): Promise<void> {
  await expectDeclinedToolRow(context.page, renderedCallId, refusal)
  await context.page.reload()
  await openWorkspace(context.page, context.workspaceId)
  await expectDeclinedToolRow(context.page, renderedCallId, refusal)
}

/** Locate every visible tool result row. */
function toolResultRows(page: Page): Locator {
  return messageBubbles(page).and(page.locator('[data-tool-row-role="result"]'))
}

/** Read the call ID of the one row of `rows`, which `what` describes for the failure message. */
async function soleRowCallId(rows: Locator, what: string): Promise<string> {
  await expect(rows, `exactly one tool result row ${what}`).toHaveCount(1)
  const callId = await rows.getAttribute('data-tool-call-id')
  if (!callId)
    throw new Error(`The tool result row that ${what} states no call ID.`)
  return callId
}

/**
 * Read the call ID of the one visible tool result row that holds `text`.
 * A provider that gives a call an ID of its own, such as Amp or Fast Agent, cannot find its row by the scripted ID, so
 * the caller finds it by text that only that row holds, such as the refusal of the call.
 */
export async function toolResultCallId(page: Page, text: string): Promise<string> {
  if (text.trim() === '')
    throw new Error('A tool result row needs text to find it, because every row holds an empty text.')
  return soleRowCallId(toolResultRows(page).filter({ hasText: text }), 'holds the text')
}

/**
 * Read the call ID of the one visible tool result row that states a declined call.
 * A provider whose result row of a refused call holds no text of the call, such as Codex, cannot find the row by its
 * text, so the caller finds it by its state. The page must show one refused call only.
 */
export async function declinedToolCallId(page: Page): Promise<string> {
  return soleRowCallId(toolResultRows(page).and(page.locator('[data-tool-status="declined"]')), 'states a declined call')
}

/**
 * How a reason that the reader types for a refusal reaches the model:
 *
 * - `native-reply`: the reply to the request carries the reason. The runtime hands it to the model in the request that
 *   continues the refused turn, as the result of the refused call.
 * - `next-message`: the reply carries no reason. LeapMux queues the reason as the reader's next message, which waits
 *   until the refused turn ends and then opens a turn of its own. The transcript draws it as a user row.
 */
export type NativeRefusalReasonRoute = 'native-reply' | 'next-message'

/** What {@link exerciseNativePermissionReason} runs around one native permission request that the reader refuses. */
export interface NativePermissionReason {
  toolCall: MockModelToolCall
  route: NativeRefusalReasonRoute
  /**
   * What the runtime does after the refusal:
   *
   * - `continues`: it asks the model again in the same turn, with the refusal as the result of the call. This is the
   *   default.
   * - `ends`: it ends the turn with no further model request.
   *
   * A reason in the native reply reaches the model only in a turn that continues.
   */
  afterRefusal?: 'continues' | 'ends'
  /** Check the banner of the request, and the target of the tool before the decision. */
  beforeDecision?: (banner: Locator) => void | Promise<void>
  /** Prove that the refused call never ran. The scenario calls it after the last turn ended. */
  expectNotRun: () => void | Promise<void>
  /**
   * Prove provider-owned view state after the last turn ended, such as the saved answer of the request. It receives
   * the reason that the reader typed.
   */
  viewProof?: (reason: string) => Promise<void>
}

/**
 * Refuse an actual native permission request with a reason that the reader types, and prove that the reason reaches
 * the model by its route.
 *
 * Text in the composer turns Deny into Send feedback, which refuses the request with that text. The scenario then
 * requires the reason in the model request that its route states. For `next-message` in a turn that continues, the
 * scenario also requires that the request of the refused turn holds no reason, because the queue holds the message
 * until the turn ends.
 */
export async function exerciseNativePermissionReason(context: NativeScenarioContext, options: NativePermissionReason): Promise<void> {
  const continues = (options.afterRefusal ?? 'continues') === 'continues'
  if (options.route === 'native-reply' && !continues)
    throw new Error('A reason in the native reply reaches the model only in a turn that continues after the refusal.')
  // A test can refuse two requests in one session, so each run gets its own reason and its own answers.
  const reason = `Leave the target as it is. ${uniqueMarker('REASON')}`
  const refusalAnswer = `The refusal reached the model. ${uniqueMarker('REFUSAL')}`
  const reasonAnswer = `The reason reached the model. ${uniqueMarker('FOLLOWUP')}`
  const steps: MockModelStep[] = [{ toolCalls: [options.toolCall] }]
  if (continues)
    steps.push(nativeTextStep(context, refusalAnswer))
  if (options.route === 'next-message')
    steps.push(nativeTextStep(context, reasonAnswer))
  const start = await context.modelScript.queue(...steps)
  await sendMessage(context.page, context.modelScript.prompt('Run the scripted permission probe.'))
  await context.modelScript.waitForSteps(start + 1)
  const banner = await waitForControlBanner(context.page)
  await options.beforeDecision?.(banner)
  await enterControlFeedback(context.page, reason)
  await expect(controlButton(context.page, 'deny')).toHaveText('Send feedback')
  await answerControl(context.page, 'deny')
  await expect(banner).toHaveCount(0)
  await context.modelScript.waitForSteps(start + steps.length)
  await waitForAgentIdle(context.page)
  await options.expectNotRun()

  const nextMessageAfterRefusal = options.route === 'next-message' && continues
  if (nextMessageAfterRefusal) {
    expect(nativeScenarioModelContextText(context, await context.modelScript.requestAt(start + 1)), 'the refused turn continues without the reason').not.toContain(reason)
  }
  const reasonRequest = await context.modelScript.requestAt(start + (nextMessageAfterRefusal ? 2 : 1))
  expect(nativeScenarioModelContextText(context, reasonRequest), 'the model reads the reason').toContain(reason)
  if (continues)
    await expect(assistantBubbles(context.page).filter({ hasText: refusalAnswer }).first()).toBeVisible()
  if (options.route === 'next-message') {
    await expect(userBubbles(context.page).filter({ hasText: reason }).first()).toBeVisible()
    await expect(assistantBubbles(context.page).filter({ hasText: reasonAnswer }).first()).toBeVisible()
  }
  await options.viewProof?.(reason)
}

/**
 * Require the saved answer of a refused request to show the reader's reason as the feedback that it sent.
 * A provider whose reply carries the reason saves the refusal this way, and the row reads `Sent feedback:` and the
 * reason.
 */
export async function expectSavedRefusalFeedback(page: Page, reason: string): Promise<void> {
  await expect(messageBubbles(page).filter({ hasText: CONTROL_RESPONSE_FEEDBACK_LEAD }).filter({ hasText: reason }).first()).toBeVisible()
}

/** What {@link exerciseRememberedAllow} runs: one allow under a remembered scope, and a later call that the rule covers. */
export interface RememberedAllow {
  /** The radio group of the scope pills. The shared permission row draws `Allow scope`, and the Codex row draws `Allow as`. */
  scopeGroup?: 'Allow scope' | 'Allow as'
  /** The label of the scope pill that keeps the rule, such as `Session` or `Always`. */
  scope: string
  /** The call that the reader allows under the scope. */
  firstCall: MockModelToolCall
  /** The later call that the kept rule covers. It runs in a turn of its own, and that turn raises no request. */
  secondCall: MockModelToolCall
  /**
   * Where each turn states its answer. The default is `next-step`. Cursor states it in the step that calls the tool,
   * because one Run exchange holds the whole turn.
   */
  answerStep?: NativeAnswerStep
  /** Check the banner of the first call, and the target before the decision. */
  beforeDecision?: (banner: Locator) => void | Promise<void>
  /**
   * Prove the native result of a call from the model request that the last step of its turn answered. In a
   * `next-step` turn that request holds the result of the call.
   */
  firstProof: (request: MockModelRequestRecord) => void | Promise<void>
  secondProof: (request: MockModelRequestRecord) => void | Promise<void>
  /** Prove provider-owned view state after both turns, such as the saved answer of the request. */
  viewProof?: () => Promise<void>
  /**
   * The files in which the native agent keeps the rule beyond the session. The scenario restores each one after it
   * ends, whether it passed or failed: the HOME of the run serves every later spec of the shard, and a kept rule would
   * answer the requests that those specs require.
   */
  ruleFiles?: readonly string[]
}

/**
 * Allow an actual native permission request under a remembered scope, then prove that the kept rule covers a later
 * call: that call runs in the next turn, and the turn raises no request at any time.
 */
export async function exerciseRememberedAllow(context: ManagedNativeScenarioContext, options: RememberedAllow): Promise<void> {
  const saved = (options.ruleFiles ?? []).map(path => ({ path, content: existsSync(path) ? readFileSync(path) : undefined }))
  await withCleanup(async () => {
    const firstAnswer = `The first call ran. ${uniqueMarker('FIRST')}`
    const firstSteps = toolTurnSteps([options.firstCall], nativeTextStep(context, firstAnswer), options.answerStep)
    const first = await context.modelScript.queue(...firstSteps)
    await sendMessage(context.page, context.modelScript.prompt('Run the scripted call once.'))
    await context.modelScript.waitForSteps(first + 1)
    const banner = await waitForControlBanner(context.page)
    await options.beforeDecision?.(banner)
    // The scope pills sit in the control actions of the composer, not in the banner.
    const scope = controlActions(context.page).getByRole('radiogroup', { name: options.scopeGroup ?? 'Allow scope' }).getByRole('radio', { name: options.scope, exact: true })
    await scope.click()
    await expect(scope).toBeChecked()
    await answerControl(context.page, 'allow')
    await expect(banner).toHaveCount(0)
    await context.modelScript.waitForSteps(first + firstSteps.length)
    await waitForAgentIdle(context.page)
    await options.firstProof(await context.modelScript.requestAt(first + firstSteps.length - 1))
    await expect(assistantBubbles(context.page).filter({ hasText: firstAnswer }).first()).toBeVisible()

    // The observation fails if a banner shows at any time in the second turn.
    await expectNoNativeControl(context, {
      testId: 'control-banner',
      relatedProof: async () => {
        const secondAnswer = `The covered call ran. ${uniqueMarker('SECOND')}`
        const secondSteps = toolTurnSteps([options.secondCall], nativeTextStep(context, secondAnswer), options.answerStep)
        const second = await context.modelScript.queue(...secondSteps)
        await sendMessage(context.page, context.modelScript.prompt('Run the scripted call again.'))
        await context.modelScript.waitForSteps(second + secondSteps.length)
        await waitForAgentIdle(context.page)
        await expectNoControlBanner(context.page)
        await options.secondProof(await context.modelScript.requestAt(second + secondSteps.length - 1))
        await expect(assistantBubbles(context.page).filter({ hasText: secondAnswer }).first()).toBeVisible()
      },
    })
    await options.viewProof?.()
  }, async () => {
    for (const { path, content } of saved) {
      if (content === undefined)
        rmSync(path, { force: true })
      else
        writeFileSync(path, content)
    }
  })
}

/**
 * Deny an actual native permission request whose runtime ends the turn after the refusal.
 * Such a runtime sends no further model request, so the scenario queues only the turn that asks for the tool.
 * A model turn queued after it would stay unconsumed.
 *
 * After the decision and after a reload, the scenario proves these facts:
 *
 * - The target did not change.
 * - The native session is the same.
 * - The Worker stored the native refusal.
 * - The model received no further request.
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
    // Read until the Worker holds the refusal row. A failed read and a snapshot that does not prove the refusal both
    // start the next attempt, and the final failure states the last error.
    await retryUntilPass(async () => options.nativeRefusal(await readNativeMessageSnapshot(context, agent.id)))
    await options.viewProof?.()
    await expectTurnEndedAfter(context.modelScript, start + 1)
  }
}

/** Require native Allow before a real file change and calculated command output. */
export async function exerciseNativePermissionWrite(
  context: ManagedNativeScenarioContext,
  options: { prepare?: () => Promise<void>, viewProof?: () => Promise<void> } = {},
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
    ...(options.viewProof ? { viewProof: options.viewProof } : {}),
  })
}

/** Run an actual write under the provider's current preset and verify its native result. */
export async function exerciseNativeToolWrite(
  context: ManagedNativeScenarioContext,
  options: { permission: 'native' | 'absent', prepare?: () => Promise<void> },
): Promise<void> {
  await options.prepare?.()
  const scenario = await nativeWriteScenario(context)
  const answer = 'The native preset write ended.'
  const run = async () => {
    const { resultRequest } = await runNativeToolTurn(context, {
      toolCalls: [scenario.toolCall],
      prompt: 'Run the scripted native preset write.',
      answer,
      permissions: options.permission === 'native' ? 'allow' : 'none',
    })
    await scenario.prove(resultRequest)
    await expect(assistantBubbles(context.page).filter({ hasText: answer }).first()).toBeVisible()
  }
  if (options.permission === 'absent')
    await expectNoNativeControl(context, { testId: 'control-banner', relatedProof: run })
  else
    await run()
}

/** Prepare calculated output and private file bytes that only native execution can produce. */
async function nativeWriteScenario(context: ManagedNativeScenarioContext, options?: NativePermissionFileOptions) {
  const agent = await currentNativeAgent(context)
  if (!agent.workingDir)
    throw new Error('The native permission write requires a working directory.')
  const marker = uniqueMarker()
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
