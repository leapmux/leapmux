import type { AgentInfo } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import type { MockModelRequestRecord, MockModelStep } from './mockModelScript'
import type { ModelScript } from './modelScriptFixture'
import type { NativeProcessOwnership } from './nativeProcessOwnership'
import type { NativeResumeTexts } from './nativeResume'
import type { ManagedNativeScenarioContext } from './nativeScenario'
import type { NativeStartupLaunch, NativeStartupWrapper } from './nativeStartupWrapper'
import type { ProcessRow } from './processTree'
import { randomUUID } from 'node:crypto'
import { existsSync, readFileSync, writeFileSync } from 'node:fs'
import { join } from 'node:path'
import process from 'node:process'
import { expect } from '@playwright/test'
import { AgentInputState, AgentStatus, ListAgentInputQueueRequestSchema, ListAgentInputQueueResponseSchema } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { TabType } from '../../../src/generated/proto/leapmux/v1/workspace_pb'
import { agentOpenOptions, agentSettings } from '../agentSettings'
import { getTestChannel, openAgentViaAPI } from './api'
import { sendNativeAnswer } from './nativeConversation'
import { resolveNativeProcessOwnership } from './nativeProcessOwnership'
import { countOriginalAnswerRows, expectReopenedNativeAgent, expectResumedConversation, nativeResumeTexts } from './nativeResume'
import { currentNativeAgent, nativeAgentById, nativeScenarioModelContextText, nativeTextStep } from './nativeScenario'
import { withNativeStartupWorker } from './nativeStartupWorker'
import { isAlive, listProcesses } from './processTree'
import { bashToolCall } from './providerToolCalls'
import { createTestDirectory } from './runDirectory'
import { getGlobalState } from './server'
import { quotePosixShellArgument } from './shellArguments'
import { assistantBubbles, messageBubbles, messageContents, openMenu, sendMessage, tabById, userBubbles, visibleOnly, waitForAgentIdle } from './ui'
import { closeAgentViaAPI, inspectLastTabCloseViaAPI, openNewAgentDialog, setWorkingDir, waitForWorker } from './worktree'

interface LifecyclePreparation {
  prepare?: () => Promise<void>
}

/** Observe the original and resumed state without interpreting provider history. */
export type NativeResumeEvidence
  = | { readonly phase: 'stored' | 'opened', readonly prior: Readonly<AgentInfo> }
    | { readonly phase: 'continued', readonly prior: Readonly<AgentInfo>, readonly request: MockModelRequestRecord }

/** The texts and the model request of one native resume scenario. */
export interface NativeResumeResult extends NativeResumeTexts {
  /** The native model request that consumed the resumed prompt. */
  readonly request: MockModelRequestRecord
}

/** Release the native model boundary after its interrupt or close scenario. */
export async function releaseNativeTurnGate(modelScript: Pick<ModelScript, 'releaseGateIfHeld'>, gate: string): Promise<void> {
  await modelScript.releaseGateIfHeld(gate)
}

/** Resume a queue only when the native interruption left it paused. */
export async function resumeInterruptedQueue(context: ManagedNativeScenarioContext): Promise<void> {
  const button = context.page.locator('[data-testid="queue-pause-button"]:visible')
  await expect(button).toHaveText('Resume Queue')
  await button.click()
  await expect(button).toHaveText('Pause Queue')
}

/**
 * The Node.js source of a held tool: it writes `startedFile`, then waits until
 * `releaseFile` exists in `workingDir` and exits with 0. It exits with 1 after
 * ten minutes, so a lost release cannot leave the process behind.
 *
 * The spaces keep each path in a short whitespace-separated word. Dirac 0.5.17
 * splits a command at whitespace and refuses it when one word that holds a `/`
 * is longer than 255 bytes (ExecuteCommandTool.validateCommands), and one word
 * with three absolute paths is longer than that.
 */
export function heldToolScript(paths: { workingDir: string, startedFile: string, releaseFile: string }): string {
  const workingDir = JSON.stringify(paths.workingDir)
  const startedFile = JSON.stringify(paths.startedFile)
  const releaseFile = JSON.stringify(paths.releaseFile)
  return [
    `const fs = require('node:fs');`,
    `fs.writeFileSync(${startedFile}, 'started');`,
    `fs.watch(${workingDir}, () => { if (fs.existsSync(${releaseFile})) process.exit(0) });`,
    `if (fs.existsSync(${releaseFile})) process.exit(0);`,
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

/** Verify native interruption and a usable next turn without changing the session. */
export async function exerciseInterruptTurn(
  context: ManagedNativeScenarioContext,
  options: LifecyclePreparation & {
    kind?: 'model' | 'tool'
    /**
     * Where a `model` turn holds: before its response (the default), or after the
     * first streamed text chunk. Goose 1.53.0 ends a turn on `session/cancel` only
     * after the response stream starts. A request that still waits for its response
     * headers runs on, and the session refuses the next prompt.
     * A `tool` turn refuses this option, because it holds the tool and not the response.
     */
    holdModelTurn?: 'before-response' | 'after-first-chunk'
    /** Applies to the `model` kind only. The default is `while-held`. */
    heldModelTurnEnd?: HeldModelTurnEnd
    prompt?: string
    divider?: RegExp
    continuation?: { prompt: string, answer: string, contextMarkers?: readonly string[] }
  } = {},
): Promise<void> {
  if (options.heldModelTurnEnd !== undefined && options.kind === 'tool')
    throw new Error('A held model turn end applies to an interrupted model request, not to an interrupted tool.')
  if (options.holdModelTurn !== undefined && options.kind === 'tool')
    throw new Error('A held model turn position applies to an interrupted model request, not to an interrupted tool.')
  await options.prepare?.()
  const marker = randomUUID().replaceAll('-', '')
  await sendNativeAnswer(context, `Keep INTERRUPTCONTEXT${marker} for this session.`, `INTERRUPTANSWER${marker}`)
  const before = await currentNativeAgent(context)
  if (!before.workingDir)
    throw new Error('The native interruption scenario requires a working directory.')
  const gate = `native-interrupt-${marker}`
  const releaseFile = join(before.workingDir, `interrupt-release-${marker}`)
  const toolStarted = join(before.workingDir, `interrupt-started-${marker}`)
  let held: MockModelStep
  if (options.kind === 'tool') {
    const script = heldToolScript({ workingDir: before.workingDir, startedFile: toolStarted, releaseFile })
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
      const allow = context.page.locator('[data-testid="control-allow-btn"]:visible').first()
      await context.modelScript.waitForSteps(stepIndex + 1)
      await expect.poll(async () => existsSync(toolStarted) || await allow.isVisible()).toBe(true)
      if (!existsSync(toolStarted))
        await allow.click()
      await expect.poll(() => existsSync(toolStarted)).toBe(true)
      await expect(messageBubbles(context.page).filter({ hasText: toolStarted }).first()).toBeVisible()
    }
    else {
      await context.modelScript.waitForGate(gate)
    }
    const interrupt = context.page.locator('[data-testid="interrupt-button"]:visible')
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
    await resumeInterruptedQueue(context)
  }
  finally {
    if (options.kind === 'tool')
      writeFileSync(releaseFile, '')
    else
      await releaseNativeTurnGate(context.modelScript, gate)
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
  const marker = randomUUID().replaceAll('-', '')
  const pidFile = join(agent.workingDir, `native-close-${marker}.pid`)
  const script = `require('node:fs').writeFileSync(${JSON.stringify(pidFile)},String(process.pid));setTimeout(()=>{},600000)`
  const stepIndex = await context.modelScript.queue({ toolCalls: [bashToolCall(context.provider, 'held-close-tool', `${quotePosixShellArgument(process.execPath)} -e ${quotePosixShellArgument(script)}`)] })
  // Some native runtimes request a cancellation continuation before their process exits.
  await context.modelScript.fallback(nativeTextStep(context, 'The native close continuation ended.'))
  await sendMessage(context.page, context.modelScript.prompt('Run the held native close probe.'))
  let toolPid = 0
  try {
    await context.modelScript.waitForSteps(stepIndex + 1)
    const allow = context.page.locator('[data-testid="control-allow-btn"]:visible').first()
    await expect.poll(async () => existsSync(pidFile) || await allow.isVisible()).toBe(true)
    if (!existsSync(pidFile))
      await allow.click()
    await expect.poll(() => existsSync(pidFile)).toBe(true)
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
    await expect.poll(() => nativeAgentById(context, agent.id)).toBeNull()
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
  const marker = randomUUID().replaceAll('-', '')
  const prompt = `Keep RESETOLDPROMPT${marker} in this session.`
  const answer = `RESETOLDANSWER${marker}`
  await sendNativeAnswer(context, prompt, answer)
  const before = await currentNativeAgent(context)
  expect(before.agentSessionId).not.toBe('')
  await sendMessage(context.page, options.command ?? '/clear')
  await expect(visibleOnly(context.page.getByText('Context cleared', { exact: true })).first()).toBeVisible()
  await expect.poll(async () => (await nativeAgentById(context, before.id))?.agentSessionId ?? '').not.toBe(before.agentSessionId)
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
  await sendNativeAnswer(context, texts.originalPrompt, texts.originalAnswer)
  const before = await currentNativeAgent(context)
  expect(before.agentSessionId).not.toBe('')
  if (options.resumeEvidence)
    await options.resumeEvidence({ phase: 'stored', prior: before })
  const originalAnswerRows = await countOriginalAnswerRows(context, before.id, texts)
  const originalAnswerBubbles = await assistantBubbles(context.page).filter({ hasText: texts.originalAnswer }).count()
  const server = context.leapmuxServer
  const keeper = await openAgentViaAPI(server.hubUrl, server.adminToken, server.workerId, context.workspaceId, createTestDirectory('native-resume-keeper-'), {
    agentProvider: context.provider,
    ...agentOpenOptions(agentSettings(context.provider)),
    title: 'Native resume keeper',
  })
  const closed = await closeAgentViaAPI(server.hubUrl, server.adminToken, server.workerId, before.id)
  expect(closed.failureMessage).toBe('')
  await tabById(context.page, keeper).click()
  await currentNativeAgent(context)
  await openNewAgentDialog(context.page)
  await waitForWorker(context.page)
  const dialog = context.page.getByRole('dialog')
  await dialog.getByTestId('agent-provider-selector-trigger').click()
  await context.page.getByTestId(`agent-provider-option-${context.provider}`).click()
  await setWorkingDir(context.page, before.workingDir)
  await openMenu(dialog, 'session-select-menu')
  const session = dialog.getByTestId(`loading-menu-option-${before.agentSessionId}`)
  await expect(session).toBeVisible()
  await session.click()
  await dialog.getByRole('button', { name: 'Create' }).click()
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
  return { ...texts, request: resumedRequest }
}

/** Prove queued startup input or a real launch failure behind a native release boundary. */
export async function exerciseAgentStartup(
  context: ManagedNativeScenarioContext,
  options: {
    launch: NativeStartupLaunch
    failed?: boolean
    prompt?: string
    answer?: string
    workingDir?: string
    onReleased?: (context: ManagedNativeScenarioContext) => Promise<void>
    workerEnvironment?: (wrapper: NativeStartupWrapper) => NodeJS.ProcessEnv
  },
): Promise<void> {
  const marker = randomUUID().replaceAll('-', '')
  const prompt = options.prompt ?? `Reply to STARTUPPROMPT${marker}.`
  const answer = options.answer ?? `STARTUPANSWER${marker}`
  if (!prompt.trim() || !answer.trim())
    throw new Error('The controlled startup prompt and answer must contain text.')
  // A failed startup sends no model request, so only a successful startup queues an answer.
  const stepIndex = options.failed ? undefined : await context.modelScript.queue(nativeTextStep(context, answer))
  await withNativeStartupWorker(context, options.launch, { failRuntime: options.failed ?? false, ...(options.workerEnvironment ? { workerEnvironment: options.workerEnvironment } : {}) }, async (workerId, wrapper) => {
    const privateContext = { ...context, leapmuxServer: { ...context.leapmuxServer, workerId } }
    const server = privateContext.leapmuxServer
    const agentId = await openAgentViaAPI(server.hubUrl, server.adminToken, workerId, context.workspaceId, options.workingDir ?? createTestDirectory('native-startup-workspace-'), {
      agentProvider: context.provider,
      ...agentOpenOptions(agentSettings(context.provider)),
      title: 'Controlled native startup',
    })
    await tabById(context.page, agentId).click()
    const channel = await getTestChannel(server.hubUrl, server.adminToken)
    const readQueue = async () => {
      const response = await channel.callWorker(workerId, 'ListAgentInputQueue', ListAgentInputQueueRequestSchema, ListAgentInputQueueResponseSchema, { agentId })
      if (!response.snapshot)
        throw new Error('The controlled startup input queue has no Worker snapshot.')
      return response.snapshot
    }
    if (!options.launch.lazy) {
      await wrapper.entry
      expect((await nativeAgentById(privateContext, agentId))?.status).toBe(AgentStatus.STARTING)
      await expect(visibleOnly(context.page.getByTestId('agent-startup-overlay'))).toBeVisible()
    }
    const editor = context.page.locator('[data-testid="composer-editor"]:visible .ProseMirror')
    await expect(editor).toBeVisible()
    await sendMessage(context.page, context.modelScript.prompt(prompt))
    await wrapper.entry
    if (options.launch.lazy) {
      // A lazy provider can accept stdin before its first process starts reading it.
      await expect.poll(async () => {
        const queue = await readQueue()
        return queue.activeTurn || queue.items.some(item => item.text.includes(prompt))
      }).toBe(true)
    }
    else {
      // The dispatcher reserves this item, then waits for native startup before it can deliver the input.
      await expect.poll(async () => {
        const queue = await readQueue()
        return queue.items.filter(item => item.text.includes(prompt)).map(item => ({ state: item.state, reserved: queue.activeTurn && !queue.activeTurnSteerable }))
      }).toEqual([{ state: AgentInputState.DISPATCHING, reserved: true }])
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
        await expect.poll(async () => (await nativeAgentById(privateContext, agentId))?.status).toBe(AgentStatus.STARTUP_FAILED)
        await expect(context.page.getByTestId('agent-startup-error')).toBeVisible()
        expect((await nativeAgentById(privateContext, agentId))?.startupError).not.toBe('')
        await expect.poll(async () => (await readQueue()).items.filter(item => item.text.includes(prompt)).map(item => item.state))
          .toEqual([AgentInputState.FAILED])
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
