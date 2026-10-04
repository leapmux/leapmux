import type { AgentInfo } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import type { MockModelRequestRecord, MockModelStep } from './mockModelScript'
import type { ModelScript } from './modelScriptFixture'
import type { NativeProcessOwnership } from './nativeProcessOwnership'
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
import { currentNativeAgent, nativeAgentById, nativeScenarioModelContextText, nativeTextStep } from './nativeScenario'
import { withNativeStartupWorker } from './nativeStartupWorker'
import { isAlive, listProcesses } from './processTree'
import { bashToolCall } from './providerToolCalls'
import { createTestDirectory } from './runDirectory'
import { getGlobalState } from './server'
import { quotePosixShellArgument } from './shellArguments'
import { assistantBubbles, messageBubbles, openMenu, sendMessage, tabById, userBubbles, visibleOnly, waitForAgentIdle } from './ui'
import { closeAgentViaAPI, inspectLastTabCloseViaAPI, openNewAgentDialog, setWorkingDir, waitForWorker } from './worktree'

interface LifecyclePreparation {
  prepare?: () => Promise<void>
}

/** Observe the original and resumed state without interpreting provider history. */
export type NativeResumeEvidence
  = | { readonly phase: 'stored' | 'opened', readonly prior: Readonly<AgentInfo> }
    | { readonly phase: 'continued', readonly prior: Readonly<AgentInfo>, readonly request: MockModelRequestRecord }

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

/** Verify native interruption and a usable next turn without changing the session. */
export async function exerciseInterruptTurn(
  context: ManagedNativeScenarioContext,
  options: LifecyclePreparation & {
    kind?: 'model' | 'tool'
    prompt?: string
    divider?: RegExp
    continuation?: { prompt: string, answer: string, contextMarkers?: readonly string[] }
  } = {},
): Promise<void> {
  await options.prepare?.()
  const marker = randomUUID().replaceAll('-', '')
  await sendNativeAnswer(context, `Keep INTERRUPTCONTEXT${marker} for this session.`, `INTERRUPTANSWER${marker}`)
  const before = await currentNativeAgent(context)
  if (!before.workingDir)
    throw new Error('The native interruption scenario requires a working directory.')
  const gate = `native-interrupt-${marker}`
  const releaseFile = join(before.workingDir, `interrupt-release-${marker}`)
  const toolStarted = join(before.workingDir, `interrupt-started-${marker}`)
  const stepIndex = (await context.modelScript.status()).stepCount
  let held: MockModelStep
  if (options.kind === 'tool') {
    const script = `const fs=require('node:fs');fs.writeFileSync(${JSON.stringify(toolStarted)},'started');fs.watch(${JSON.stringify(before.workingDir)},()=>{if(fs.existsSync(${JSON.stringify(releaseFile)}))process.exit(0)});if(fs.existsSync(${JSON.stringify(releaseFile)}))process.exit(0);setTimeout(()=>process.exit(1),600000)`
    held = { toolCalls: [bashToolCall(context.provider, 'held-native-tool', `${quotePosixShellArgument(process.execPath)} -e ${quotePosixShellArgument(script)}`)] }
  }
  else {
    held = { ...nativeTextStep(context, `NEVERCOMPLETED${marker}`), gate }
  }
  try {
    await context.modelScript.queue(held)
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
    const divider = context.page.locator('[data-testid="result-divider"]:visible').last()
    if (options.divider)
      await expect(divider).toHaveText(options.divider)
    else
      await expect(divider).toContainText('interrupted')
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
  const stepIndex = (await context.modelScript.status()).stepCount
  await context.modelScript.queue({ toolCalls: [bashToolCall(context.provider, 'held-close-tool', `${quotePosixShellArgument(process.execPath)} -e ${quotePosixShellArgument(script)}`)] })
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

/** Reopen a stored native session and restore the Worker transcript through the picker. */
export async function exerciseSessionResume(
  context: ManagedNativeScenarioContext,
  options: LifecyclePreparation & {
    resumeEvidence?: (evidence: NativeResumeEvidence) => Promise<void>
  } = {},
): Promise<MockModelRequestRecord> {
  await options.prepare?.()
  const marker = randomUUID().replaceAll('-', '')
  const prompt = `Keep RESUMEPROMPT${marker} for the stored session.`
  const answer = `RESUMEANSWER${marker}`
  await sendNativeAnswer(context, prompt, answer)
  const before = await currentNativeAgent(context)
  expect(before.agentSessionId).not.toBe('')
  if (options.resumeEvidence)
    await options.resumeEvidence({ phase: 'stored', prior: before })
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
  await expect(userBubbles(context.page).filter({ hasText: prompt })).toHaveCount(1)
  await expect(assistantBubbles(context.page).filter({ hasText: answer })).toHaveCount(1)
  const resumedRequest = await sendNativeAnswer(context, 'Reply after the native picker reopens the session.', `RESUMEDNEWANSWER${marker}`)
  expect((await currentNativeAgent(context)).agentSessionId).toBe(before.agentSessionId)
  if (options.resumeEvidence)
    await options.resumeEvidence({ phase: 'continued', prior: before, request: resumedRequest })
  return resumedRequest
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
  const stepIndex = (await context.modelScript.status()).stepCount
  if (!options.failed)
    await context.modelScript.queue(nativeTextStep(context, answer))
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
    if (options.failed) {
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
      const request = (await context.modelScript.status()).requests.find(record => record.stepIndex === stepIndex)
      if (!request)
        throw new Error('The controlled startup input reached no native model request.')
      expect(nativeScenarioModelContextText(context, request)).toContain(prompt)
      await expect(userBubbles(context.page).filter({ hasText: prompt }).first()).toBeVisible()
      await expect(assistantBubbles(context.page).filter({ hasText: answer }).first()).toBeVisible()
      await expect(visibleOnly(context.page.getByTestId('agent-startup-overlay'))).not.toBeVisible()
      expect((await readQueue()).items).toEqual([])
      await expect(context.page.locator('[data-testid="agent-input-queue"]:visible')).toHaveCount(0)
    }
  })
}
