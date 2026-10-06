import type { AgentInfo } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import type { MockModelRequestRecord, MockModelStep } from './mockModelScript'
import type { NativeProcessOwnership } from './nativeProcessOwnership'
import type { NativeResumeTexts } from './nativeResume'
import type { ManagedNativeScenarioContext } from './nativeScenario'
import type { NativeStartupLaunch, NativeStartupWrapper } from './nativeStartupWrapper'
import type { ProcessRow } from './processTree'
import { existsSync, readFileSync, writeFileSync } from 'node:fs'
import { join } from 'node:path'
import process from 'node:process'
import { expect } from '@playwright/test'
import { AgentInputState, AgentStatus } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { TabType } from '../../../src/generated/proto/leapmux/v1/workspace_pb'
import { agentOpenOptions } from '../agentSettings'
import { openAgentViaAPI } from './api'
import { newNativeWorkingDir } from './nativeAgentOpen'
import { sendNativeAnswer } from './nativeConversation'
import { readNativeInputQueue } from './nativeInputQueueIdle'
import { resolveNativeProcessOwnership } from './nativeProcessOwnership'
import { countOriginalAnswerRows, expectReopenedNativeAgent, expectResumedConversation, nativeResumeTexts, reopenFromSessionPicker } from './nativeResume'
import { currentNativeAgent, nativeAgentById, nativeScenarioModelContextText, nativeTextStep } from './nativeScenario'
import { withNativeStartupWorker } from './nativeStartupWorker'
import { approveNativeToolsUntil } from './nativeToolExecution'
import { isAlive, listProcesses } from './processTree'
import { bashToolCall } from './providerToolCalls'
import { deliberateWorkingDir } from './providerWorkingDir'
import { retryUntilPass } from './retryUntilPass'
import { getGlobalState } from './server'
import { quotePosixShellArgument, uniqueMarker } from './shellArguments'
import { RELEASE_POLL_MS } from './toolOutputControl'
import { assistantBubbles, composerEditor, interruptButton, messageBubbles, messageContents, resumePausedQueue, sendMessage, tabById, userBubbles, visibleOnly, waitForAgentIdle } from './ui'
import { closeNativeAgentAndWait, inspectLastTabCloseViaAPI } from './workerTabs'

interface LifecyclePreparation {
  prepare?: () => Promise<void>
}

/** Observe the original and resumed state without interpreting provider history. */
export type NativeResumeEvidence
  = | { readonly phase: 'stored' | 'opened', readonly prior: Readonly<AgentInfo> }
    | { readonly phase: 'continued', readonly prior: Readonly<AgentInfo>, readonly request: MockModelRequestRecord }

/** The texts and the model requests of one native resume scenario. */
export interface NativeResumeResult extends NativeResumeTexts {
  /** The native model request that consumed the original prompt, read after its turn ended. */
  readonly originalRequest: MockModelRequestRecord
  /** The native model request that consumed the resumed prompt. */
  readonly request: MockModelRequestRecord
}

/**
 * The Node.js source of a held tool: it writes `startedFile`, then waits until
 * `releaseFile` exists and exits with 0. It exits with 1 after ten minutes, so a
 * lost release cannot leave the process behind.
 *
 * It polls for the release file and does not watch its directory, for the reason
 * that {@link RELEASE_POLL_MS} states: a sandbox can refuse a watch.
 *
 * The spaces keep each path in a short whitespace-separated word. Dirac 0.5.17
 * splits a command at whitespace and refuses it when one word that holds a `/`
 * is longer than 255 bytes (ExecuteCommandTool.validateCommands), and one word
 * with two absolute paths is longer than that.
 *
 * The held command cannot be a one-segment `createToolOutputControl`
 * (`./toolOutputControl.ts`), although both poll the same way. The control
 * passes its script as one base64 word, which can hold a `/` and is longer than
 * 255 bytes, so Dirac refuses it. The control also runs a file whose path the
 * command shows, while the interrupt scenario finds the tool row by the started
 * file path that this command shows.
 */
export function heldToolScript(paths: { startedFile: string, releaseFile: string }): string {
  const startedFile = JSON.stringify(paths.startedFile)
  const releaseFile = JSON.stringify(paths.releaseFile)
  return [
    `const fs = require('node:fs');`,
    `fs.writeFileSync(${startedFile}, 'started');`,
    `const release = () => { if (fs.existsSync(${releaseFile})) process.exit(0) };`,
    `setInterval(release, ${RELEASE_POLL_MS});`,
    `release();`,
    `setTimeout(() => process.exit(1), 600000)`,
  ].join(' ')
}

/**
 * When the native runtime ends a turn that the reader interrupted during a held model request.
 *
 * - `while-held`: the runtime cancels the request and ends the turn at once.
 * - `after-answer`: the runtime does not cancel a request that is in flight. It stops its
 *   loop at once, but it ends the turn only when the held answer arrives, and it then
 *   drops that answer. Letta Code 0.34.2 works this way.
 */
export type HeldModelTurnEnd = 'while-held' | 'after-answer'

/** The options that both kinds of interrupted turn take. */
interface InterruptTurnCommonOptions extends LifecyclePreparation {
  prompt?: string
  divider?: RegExp
  continuation?: { prompt: string, answer: string, contextMarkers?: readonly string[] }
}

/** Interrupt a held model request. This is the default kind. */
interface InterruptedModelTurnOptions extends InterruptTurnCommonOptions {
  kind?: 'model'
  /**
   * Where the turn holds: before its response (the default), or after the
   * first streamed text chunk. Goose 1.53.0 ends a turn on `session/cancel` only
   * after the response stream starts. A request that still waits for its response
   * headers runs on, and the session refuses the next prompt.
   */
  holdModelTurn?: 'before-response' | 'after-first-chunk'
  /** When the runtime ends the interrupted turn. The default is `while-held`. */
  heldModelTurnEnd?: HeldModelTurnEnd
}

/**
 * Interrupt a running native tool.
 * A tool turn holds the tool and not the model response, so the two model options do not apply, and the type refuses
 * them.
 */
interface InterruptedToolTurnOptions extends InterruptTurnCommonOptions {
  kind: 'tool'
  holdModelTurn?: never
  heldModelTurnEnd?: never
}

/** How `exerciseInterruptTurn` holds the turn that it interrupts. */
export type InterruptTurnOptions = InterruptedModelTurnOptions | InterruptedToolTurnOptions

/** Verify native interruption and a usable next turn without changing the session. */
export async function exerciseInterruptTurn(
  context: ManagedNativeScenarioContext,
  options: InterruptTurnOptions = {},
): Promise<void> {
  // The type refuses these combinations. The checks stay for a caller that builds its options as a wider type,
  // because the tool branch would otherwise ignore the model options with no message. The checks read one view
  // that is not a union, because the union narrows each comparison of `kind` to a branch that cannot hold it.
  const requested: { kind?: string, holdModelTurn?: unknown, heldModelTurnEnd?: unknown } = options
  if (requested.heldModelTurnEnd !== undefined && requested.kind === 'tool')
    throw new Error('A held model turn end applies to an interrupted model request, not to an interrupted tool.')
  if (requested.holdModelTurn !== undefined && requested.kind === 'tool')
    throw new Error('A held model turn position applies to an interrupted model request, not to an interrupted tool.')
  await options.prepare?.()
  const marker = uniqueMarker()
  await sendNativeAnswer(context, `Keep INTERRUPTCONTEXT${marker} for this session.`, `INTERRUPTANSWER${marker}`)
  const before = await currentNativeAgent(context)
  if (!before.workingDir)
    throw new Error('The native interruption scenario requires a working directory.')
  const gate = `native-interrupt-${marker}`
  const releaseFile = join(before.workingDir, `interrupt-release-${marker}`)
  const toolStarted = join(before.workingDir, `interrupt-started-${marker}`)
  let held: MockModelStep
  if (options.kind === 'tool') {
    const script = heldToolScript({ startedFile: toolStarted, releaseFile })
    held = { toolCalls: [bashToolCall(context.provider, 'held-native-tool', `${quotePosixShellArgument(process.execPath)} -e ${quotePosixShellArgument(script)}`)] }
  }
  else if (options.holdModelTurn === 'after-first-chunk') {
    const text = `NEVERCOMPLETED${marker}`
    held = { ...nativeTextStep(context, text), text, stream: { chunkChars: 16, delayMs: 0, gates: [{ afterChunk: 1, name: gate }] } }
  }
  else {
    held = { ...nativeTextStep(context, `NEVERCOMPLETED${marker}`), gate }
  }
  try {
    const stepIndex = await context.modelScript.queue(held)
    context.modelScript.allowUnconsumed('The native interruption ends the held turn before its answer completes.')
    await sendMessage(context.page, context.modelScript.prompt(options.prompt ?? 'Run the held native interruption probe.'))
    if (options.kind === 'tool') {
      await context.modelScript.waitForSteps(stepIndex + 1)
      // A provider that asks before a tool runs shows an Allow button. The wait allows each actual request.
      await approveNativeToolsUntil(context.page, async () => existsSync(toolStarted))
      await expect(messageBubbles(context.page).filter({ hasText: toolStarted }).first()).toBeVisible()
    }
    else {
      await context.modelScript.waitForGate(gate)
    }
    const interrupt = interruptButton(context.page)
    await expect(interrupt).toBeVisible()
    await interrupt.click()
    await waitForAgentIdle(context.page)
    const answerArrivesAfterStop = options.kind !== 'tool' && options.heldModelTurnEnd === 'after-answer'
    // The runtime stopped while the request still waits. `releaseGate` fails when no
    // request waits, so a runtime that cancels the request fails here and states that
    // the option no longer applies.
    if (answerArrivesAfterStop)
      await context.modelScript.releaseGate(gate)
    const divider = context.page.locator('[data-testid="result-divider"]:visible').last()
    if (options.divider)
      await expect(divider).toHaveText(options.divider)
    else
      await expect(divider).toContainText('interrupted')
    // The divider states that the runtime ended the turn, so it read the late answer
    // already. That answer belongs to a cancelled turn, and no row may draw it.
    if (answerArrivesAfterStop)
      await expect(messageContents(context.page).filter({ hasText: `NEVERCOMPLETED${marker}` })).toHaveCount(0)
    // The divider states that the turn ended, so the button goes with the turn, and nothing is left to press.
    await expect(interrupt).toHaveCount(0)
    // A stop always pauses the queue, so the next prompt waits until the queue resumes.
    await resumePausedQueue(context.page)
  }
  finally {
    if (options.kind === 'tool')
      writeFileSync(releaseFile, '')
    else
      await context.modelScript.releaseGateIfHeld(gate)
  }
  const next = await sendNativeAnswer(context, options.continuation?.prompt ?? 'Continue after the native interruption.', options.continuation?.answer ?? `AFTERINTERRUPT${marker}`)
  expect(nativeScenarioModelContextText(context, next)).toContain(`INTERRUPTANSWER${marker}`)
  for (const contextMarker of options.continuation?.contextMarkers ?? [])
    expect(nativeScenarioModelContextText(context, next)).toContain(contextMarker)
  expect((await currentNativeAgent(context)).agentSessionId).toBe(before.agentSessionId)
}

/** Read the native process ownership of one actual held tool. */
function ownedProcesses(rows: readonly ProcessRow[], toolPid: number) {
  return resolveNativeProcessOwnership(rows, toolPid, getGlobalState().binaryPath)
}

/** Verify Worker-confirmed close and cleanup of a real native tool and its provider ancestors. */
export async function exerciseCloseAgent(
  context: ManagedNativeScenarioContext,
  options: LifecyclePreparation & {
    nativeOwnership?: (proof: { rows: readonly ProcessRow[], toolPid: number, ownership: NativeProcessOwnership }) => void | Promise<void>
  } = {},
): Promise<void> {
  await options.prepare?.()
  const agent = await currentNativeAgent(context)
  if (!agent.workingDir)
    throw new Error('The native close scenario requires a working directory.')
  const marker = uniqueMarker()
  const pidFile = join(agent.workingDir, `native-close-${marker}.pid`)
  // The probe needs no release file: the close of the agent must end it, and the scenario proves that it did. The
  // timer only limits a process that a failed close leaves behind, and the cleanup below ends that process as well.
  const script = `require('node:fs').writeFileSync(${JSON.stringify(pidFile)},String(process.pid));setTimeout(()=>{},600000)`
  const stepIndex = await context.modelScript.queue({ toolCalls: [bashToolCall(context.provider, 'held-close-tool', `${quotePosixShellArgument(process.execPath)} -e ${quotePosixShellArgument(script)}`)] })
  // Some native runtimes request a cancellation continuation before their process exits.
  await context.modelScript.fallback(nativeTextStep(context, 'The native close continuation ended.'))
  await sendMessage(context.page, context.modelScript.prompt('Run the held native close probe.'))
  let toolPid = 0
  try {
    await context.modelScript.waitForSteps(stepIndex + 1)
    await approveNativeToolsUntil(context.page, async () => existsSync(pidFile))
    toolPid = Number(readFileSync(pidFile, 'utf8'))
    expect(Number.isInteger(toolPid) && toolPid > 0).toBe(true)
    const rows = listProcesses()
    const ownership = ownedProcesses(rows, toolPid)
    const owned = ownership.ownedPids
    expect(owned).toContain(toolPid)
    await options.nativeOwnership?.({ rows, toolPid, ownership })
    const server = context.leapmuxServer
    const inspection = await inspectLastTabCloseViaAPI(server.hubUrl, server.adminToken, server.workerId, TabType.AGENT, agent.id)
    await tabById(context.page, agent.id).getByTestId('tab-close').click()
    const dialog = inspection.shouldPrompt
      ? context.page.getByRole('dialog').filter({ has: context.page.getByRole('heading', { name: 'Close last tab', exact: true }) })
      : context.page.getByTestId('busy-tab-close-dialog')
    await expect(dialog).toBeVisible()
    await dialog.getByRole('button', { name: 'Close anyway', exact: true }).click()
    await dialog.getByRole('button', { name: 'Confirm?', exact: true }).click()
    await expect.poll(() => owned.filter(isAlive)).toEqual([])
    expect(isAlive(ownership.workerPid)).toBe(true)
    // ListAgents omits a row only after the Worker records its completed close.
    await retryUntilPass(async () => {
      expect(await nativeAgentById(context, agent.id), 'the Worker lists the closed agent no more').toBeNull()
    })
  }
  finally {
    if (toolPid > 0 && isAlive(toolPid))
      process.kill(toolPid, 'SIGTERM')
  }
}

/** Prove that the selected reset command starts a new native context and preserves the old LeapMux rows. */
export async function exerciseSessionReset(
  context: ManagedNativeScenarioContext,
  options: LifecyclePreparation & { command?: '/clear' | '/reset' } = {},
): Promise<void> {
  await options.prepare?.()
  const marker = uniqueMarker()
  const prompt = `Keep RESETOLDPROMPT${marker} in this session.`
  const answer = `RESETOLDANSWER${marker}`
  await sendNativeAnswer(context, prompt, answer)
  const before = await currentNativeAgent(context)
  expect(before.agentSessionId).not.toBe('')
  await sendMessage(context.page, options.command ?? '/clear')
  await expect(visibleOnly(context.page.getByText('Context cleared', { exact: true })).first()).toBeVisible()
  await retryUntilPass(async () => {
    expect((await nativeAgentById(context, before.id))?.agentSessionId ?? '', 'the Worker starts a new native session').not.toBe(before.agentSessionId)
  })
  const next = await sendNativeAnswer(context, 'Reply in the new native session.', `RESETNEWANSWER${marker}`)
  const nativeContext = nativeScenarioModelContextText(context, next)
  expect(nativeContext).not.toContain(prompt)
  expect(nativeContext).not.toContain(answer)
  await expect(userBubbles(context.page).filter({ hasText: prompt }).first()).toBeVisible()
  await expect(assistantBubbles(context.page).filter({ hasText: answer }).first()).toBeVisible()
}

/**
 * Reopen a stored native session through the picker and restore the Worker transcript. Then prove these conditions:
 * - The Worker started the reopened agent in the stored native session.
 * - The continued turn reached the model.
 * - The continued turn left one copy of each original row and a separate resumed answer.
 */
export async function exerciseSessionResume(
  context: ManagedNativeScenarioContext,
  options: LifecyclePreparation & {
    resumeEvidence?: (evidence: NativeResumeEvidence) => Promise<void>
  } = {},
): Promise<NativeResumeResult> {
  await options.prepare?.()
  const texts = nativeResumeTexts()
  const originalRequest = await sendNativeAnswer(context, texts.originalPrompt, texts.originalAnswer)
  const before = await currentNativeAgent(context)
  expect(before.agentSessionId).not.toBe('')
  // The context holds no directory of its agent, so the directory comes from the Worker. The check runs before the
  // keeper opens and the original agent closes.
  const storedDir = deliberateWorkingDir(
    before.workingDir,
    'The picker lists the stored session in the directory where the Worker ran the original agent, so the reopened agent opens there.',
  )
  if (options.resumeEvidence)
    await options.resumeEvidence({ phase: 'stored', prior: before })
  const originalAnswerRows = await countOriginalAnswerRows(context, before.id, texts)
  const originalAnswerBubbles = await assistantBubbles(context.page).filter({ hasText: texts.originalAnswer }).count()
  const server = context.leapmuxServer
  const keeper = await openAgentViaAPI(server, context.workspaceId, newNativeWorkingDir(context, 'native-resume-keeper-'), {
    ...agentOpenOptions(context.provider),
    title: 'Native resume keeper',
  })
  await closeNativeAgentAndWait(context, before.id)
  await tabById(context.page, keeper).click()
  await currentNativeAgent(context)
  await reopenFromSessionPicker(context.page, { provider: context.provider, workingDir: storedDir, sessionId: before.agentSessionId })
  if (options.resumeEvidence)
    await options.resumeEvidence({ phase: 'opened', prior: before })
  const reopened = await expectReopenedNativeAgent(context, before, [before.id, keeper])
  await expect(userBubbles(context.page).filter({ hasText: texts.originalPrompt })).toHaveCount(1)
  // The reopened transcript draws exactly the answer bubbles the live one drew
  // before the close: an answer tool can leave the answer in several stored row
  // kinds (Dirac's `respond` request, result and text rows), and rows that render
  // no bubble keep the page count its own number, not the row count.
  await expect(assistantBubbles(context.page).filter({ hasText: texts.originalAnswer })).toHaveCount(originalAnswerBubbles)
  const resumedRequest = await sendNativeAnswer(context, texts.resumedPrompt, texts.resumedAnswer)
  const continued = await currentNativeAgent(context)
  expect(continued.id).toBe(reopened.id)
  expect(continued.agentSessionId).toBe(before.agentSessionId)
  if (options.resumeEvidence)
    await options.resumeEvidence({ phase: 'continued', prior: before, request: resumedRequest })
  await expectResumedConversation(context, reopened.id, texts, originalAnswerRows, originalAnswerBubbles)
  return { ...texts, originalRequest, request: resumedRequest }
}

/**
 * Prove queued startup input or a real launch failure behind a native release boundary.
 * The agent opens on a private Worker, in a new working directory by the rule of the provider of the context.
 */
export async function exerciseAgentStartup(
  context: ManagedNativeScenarioContext,
  options: {
    launch: NativeStartupLaunch
    failed?: boolean
    prompt?: string
    answer?: string
    onReleased?: (context: ManagedNativeScenarioContext) => Promise<void>
    workerEnvironment?: (wrapper: NativeStartupWrapper) => NodeJS.ProcessEnv
  },
): Promise<void> {
  const marker = uniqueMarker()
  const prompt = options.prompt ?? `Reply to STARTUPPROMPT${marker}.`
  const answer = options.answer ?? `STARTUPANSWER${marker}`
  if (!prompt.trim() || !answer.trim())
    throw new Error('The controlled startup prompt and answer must contain text.')
  // A failed startup sends no model request, so only a successful startup queues an answer.
  const stepIndex = options.failed ? undefined : await context.modelScript.queue(nativeTextStep(context, answer))
  await withNativeStartupWorker(context, options.launch, { failRuntime: options.failed ?? false, ...(options.workerEnvironment ? { workerEnvironment: options.workerEnvironment } : {}) }, async (workerId, wrapper) => {
    const privateContext = { ...context, leapmuxServer: { ...context.leapmuxServer, workerId } }
    const server = privateContext.leapmuxServer
    const agentId = await openAgentViaAPI(server, context.workspaceId, newNativeWorkingDir(context, 'native-startup-workspace-'), {
      ...agentOpenOptions(context.provider),
      title: 'Controlled native startup',
    })
    await tabById(context.page, agentId).click()
    const readQueue = () => readNativeInputQueue(server, agentId)
    if (!options.launch.lazy) {
      await wrapper.entry
      expect((await nativeAgentById(privateContext, agentId))?.status).toBe(AgentStatus.STARTING)
      await expect(visibleOnly(context.page.getByTestId('agent-startup-overlay'))).toBeVisible()
    }
    const editor = composerEditor(context.page)
    await expect(editor).toBeVisible()
    await sendMessage(context.page, context.modelScript.prompt(prompt))
    await wrapper.entry
    if (options.launch.lazy) {
      // A lazy provider can accept stdin before its first process starts reading it.
      await retryUntilPass(async () => {
        const queue = await readQueue()
        expect(queue.activeTurn || queue.items.some(item => item.text.includes(prompt)), 'the Worker started the turn or queued the prompt').toBe(true)
      })
    }
    else {
      // The dispatcher reserves this item, then waits for native startup before it can deliver the input.
      await retryUntilPass(async () => {
        const queue = await readQueue()
        expect(
          queue.items.filter(item => item.text.includes(prompt)).map(item => ({ state: item.state, reserved: queue.activeTurn && !queue.activeTurnSteerable })),
          'the Worker reserves the queued prompt while native startup waits',
        ).toEqual([{ state: AgentInputState.DISPATCHING, reserved: true }])
      })
      await expect(context.page.getByTestId('agent-input-queue')).toContainText(prompt)
    }
    await expect(editor).toHaveText('')
    expect((await context.modelScript.status()).requests.filter(request => nativeScenarioModelContextText(context, request).includes(prompt))).toEqual([])
    await wrapper.release()
    await options.onReleased?.(privateContext)
    if (stepIndex === undefined) {
      if (options.launch.lazy) {
        await waitForAgentIdle(context.page)
        await expect(visibleOnly(context.page.getByText(/Native startup failed:/)).first()).toBeVisible()
        await expect(userBubbles(context.page).filter({ hasText: prompt }).first()).toBeVisible()
      }
      else {
        await retryUntilPass(async () => {
          expect((await nativeAgentById(privateContext, agentId))?.status, 'the Worker reports the failed startup').toBe(AgentStatus.STARTUP_FAILED)
        })
        await expect(context.page.getByTestId('agent-startup-error')).toBeVisible()
        expect((await nativeAgentById(privateContext, agentId))?.startupError).not.toBe('')
        await retryUntilPass(async () => {
          expect((await readQueue()).items.filter(item => item.text.includes(prompt)).map(item => item.state), 'the Worker fails the queued prompt')
            .toEqual([AgentInputState.FAILED])
        })
        await expect(context.page.getByTestId('agent-input-queue')).toContainText(prompt)
        await expect(context.page.getByTestId('agent-input-queue')).toContainText('Failed')
      }
      expect((await context.modelScript.status()).requests.filter(request => nativeScenarioModelContextText(context, request).includes(prompt))).toEqual([])
    }
    else {
      await context.modelScript.waitForSteps(stepIndex + 1)
      await waitForAgentIdle(context.page)
      const request = await context.modelScript.requestAt(stepIndex)
      expect(nativeScenarioModelContextText(context, request)).toContain(prompt)
      await expect(userBubbles(context.page).filter({ hasText: prompt }).first()).toBeVisible()
      await expect(assistantBubbles(context.page).filter({ hasText: answer }).first()).toBeVisible()
      await expect(visibleOnly(context.page.getByTestId('agent-startup-overlay'))).not.toBeVisible()
      expect((await readQueue()).items).toEqual([])
      await expect(context.page.locator('[data-testid="agent-input-queue"]:visible')).toHaveCount(0)
    }
  })
}
