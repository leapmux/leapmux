import type { Locator, Page } from '@playwright/test'
import type { MockModelRequestRecord, MockModelStep, MockModelToolCall } from './mockModelScript'
import type { ModelScript } from './modelScriptFixture'
import type { ManagedNativeScenarioContext, NativeScenarioContext, NativeToolResultReader } from './nativeScenario'
import { existsSync, readFileSync, writeFileSync } from 'node:fs'
import { isAbsolute, join } from 'node:path'
import { expect } from '@playwright/test'
import { currentNativeAgent, nativeTextStep, nativeToolOutcome } from './nativeScenario'
import { createNativeToolDirectory } from './nativeToolDirectory'
import { nativeToolResult } from './nativeToolResult'
import { createOutputGate, runWithGatedOutput } from './outputGate'
import { bashToolCall, editToolCall, readToolCall, writeToolCall } from './providerToolCalls'
import { isFileNameComponent } from './runDirectory'
import { printfMarkerCommand, quotePosixShellArgument, uniqueMarker } from './shellArguments'
import { assistantBubbles, controlButton, expectNoControlBanner, messageBubbles, messageContents, sendMessage, waitForAgentIdle } from './ui'

interface ToolPreparation {
  prepare?: () => Promise<void>
}

interface NativeToolApprovalOperation {
  completed: () => Promise<boolean>
  clickIfReady: () => Promise<boolean>
}

/** Check and click the same actual button in one browser operation. */
export function clickNativeToolApproval(elements: Element[], approvalAllowed = true): boolean {
  if (elements.length > 1)
    throw new Error('The native approval operation requires one selected control.')
  const button = elements[0]
  if (!button)
    return false
  if (!(button instanceof HTMLButtonElement))
    throw new Error('The native approval control must be an actual button.')
  const style = globalThis.getComputedStyle(button)
  if (!button.isConnected || button.hidden || button.matches(':disabled') || button.getClientRects().length === 0
    || style.display === 'none' || style.visibility === 'hidden' || style.visibility === 'collapse') {
    return false
  }
  if (!approvalAllowed)
    throw new Error('The native tool scenario exceeded its approval limit.')
  button.click()
  return true
}

/** Process the actual native approval and the exact model completion receipt. */
export async function processNativeToolApproval(control: NativeToolApprovalOperation): Promise<'completed' | 'approval' | 'waiting'> {
  if (await control.completed())
    return 'completed'
  if (await control.clickIfReady())
    return 'approval'
  return await control.completed() ? 'completed' : 'waiting'
}

/** The most native approvals that one wait allows. A turn that asks more often than this has a defect. */
export const NATIVE_APPROVAL_LIMIT = 16

/**
 * Allow each actual native approval request until `completed` reports true.
 * `allow` locates the Allow button. The default is the first visible Allow button of the page:
 * `clickNativeToolApproval` checks and clicks one button in one browser operation, so the loop reads one button.
 * A page that shows more than one agent passes a locator that is scoped to the agent.
 * The wait fails when the agent asks for more than {@link NATIVE_APPROVAL_LIMIT} approvals.
 */
export async function approveNativeToolsUntil(page: Page, completed: () => Promise<boolean>, allow: Locator = controlButton(page, 'allow').first()): Promise<void> {
  let approvals = 0
  while (!await completed()) {
    await expect.poll(async () => {
      return processNativeToolApproval({
        completed,
        clickIfReady: async () => {
          const clicked = await allow.evaluateAll(clickNativeToolApproval, approvals < NATIVE_APPROVAL_LIMIT)
          if (clicked)
            approvals++
          return clicked
        },
      })
    }).not.toBe('waiting')
  }
}

/** Allow only actual native tool approval requests until the script completes. */
export async function waitForNativeToolSteps(context: NativeScenarioContext, target: number, options: { beforeIdle?: () => Promise<void> } = {}): Promise<void> {
  await approveNativeToolsUntil(context.page, async () => (await context.modelScript.status()).nextStep >= target)
  await context.modelScript.waitForSteps(target)
  await options.beforeIdle?.()
  await waitForAgentIdle(context.page)
}

/** One tool step of a native turn: the tool calls of one model answer. */
export interface NativeToolStep {
  toolCalls: readonly MockModelToolCall[]
  /** Text that the step captures from its own request, for a `{{name}}` placeholder in a tool call. */
  captures?: Readonly<Record<string, string>>
}

/**
 * How a native tool turn treats a native permission request:
 *
 * - `allow`: the turn clicks Allow on each request, through {@link waitForNativeToolSteps}.
 * - `none`: the mode of the agent runs each tool with no request, and the turn clicks nothing. A request holds the
 *   turn, so the wait for its steps fails. After the turn, the page must hold no control request banner.
 */
export type NativeToolPermissions = 'allow' | 'none'

/** One native turn: one or more model steps that call tools, then a model step that answers. */
export interface NativeToolStepsTurn {
  /** The tool steps, in the order that the model answers them. */
  steps: readonly NativeToolStep[]
  /** The prompt that starts the turn. The turn marks it for this test's script. */
  prompt: string
  /** The final answer. It goes through `nativeTextStep`, so a provider that answers through a tool keeps its own form. */
  answer: string
  /** Send the marked prompt. `sendMessage` by default; a turn that attaches a file passes its own sender. */
  send?: (page: Page, text: string) => Promise<void>
  /** How the turn treats a native permission request. The default is `allow`. */
  permissions?: NativeToolPermissions
}

/** One native turn: a model step that calls tools, then a model step that answers. */
export type NativeToolTurn = NativeToolStep & Omit<NativeToolStepsTurn, 'steps'>

/** The requests of one {@link NativeToolTurn}. */
export interface NativeToolTurnRequests {
  /** The step index of the tool step. The answer step is `start + 1`. */
  start: number
  /** The request that the tool step answered. It holds the tool catalog that the native client offered. */
  toolRequest: MockModelRequestRecord
  /** The request that the answer step answered. It holds the results of the tool calls. */
  resultRequest: MockModelRequestRecord
}

/** The model step that answers one {@link NativeToolStep}. The copies keep the caller's arrays out of the script. */
function nativeToolModelStep(step: NativeToolStep): MockModelStep {
  return { toolCalls: [...step.toolCalls], ...(step.captures ? { captures: { ...step.captures } } : {}) }
}

/**
 * Run one native turn of one or more tool steps, and return the step index of the first tool step.
 * The request at `start + n` holds the results of the step at `start + n - 1`, and the answer step is
 * `start + steps.length`. The turn treats a permission request as `permissions` states, and it ends after the agent
 * is idle. A turn that answers a banner or a form between its steps cannot use this helper.
 */
export async function runNativeToolSteps(context: NativeScenarioContext, turn: NativeToolStepsTurn): Promise<number> {
  if (turn.steps.length === 0)
    throw new Error('A native tool turn needs at least one tool step.')
  if (turn.steps.some(step => step.toolCalls.length === 0))
    throw new Error('Each tool step of a native turn needs at least one tool call.')
  const start = await context.modelScript.queue(...turn.steps.map(nativeToolModelStep), nativeTextStep(context, turn.answer))
  await (turn.send ?? sendMessage)(context.page, context.modelScript.prompt(turn.prompt))
  const target = start + turn.steps.length + 1
  if ((turn.permissions ?? 'allow') === 'allow') {
    await waitForNativeToolSteps(context, target)
    return start
  }
  await context.modelScript.waitForSteps(target)
  await waitForAgentIdle(context.page)
  await expectNoControlBanner(context.page)
  return start
}

/**
 * Run one native tool turn and return both of its model requests.
 * The turn treats a permission request as `permissions` states, and it ends after the agent is idle.
 * A turn that answers a banner or a form between its two steps cannot use this helper.
 */
export async function runNativeToolTurn(context: NativeScenarioContext, turn: NativeToolTurn): Promise<NativeToolTurnRequests> {
  const { toolCalls, captures, ...rest } = turn
  const start = await runNativeToolSteps(context, { ...rest, steps: [{ toolCalls, ...(captures ? { captures } : {}) }] })
  return {
    start,
    toolRequest: await context.modelScript.requestAt(start),
    resultRequest: await context.modelScript.requestAt(start + 1),
  }
}

/** Read one actual queued tool result. Call arguments cannot prove its returned answer. */
export async function nativeToolResultAt(modelScript: Pick<ModelScript, 'requestAt'>, stepIndex: number, callId: string): Promise<string> {
  if (!callId)
    throw new Error('The native result query requires a tool call ID.')
  return nativeToolResult(await modelScript.requestAt(stepIndex), callId)
}

/** Require the exact native Read output. Scripted edit arguments and earlier reads cannot prove it. */
export async function nativeFileReadResult(
  request: MockModelRequestRecord,
  callId: string,
  expected: string,
  excluded: string,
  reader?: NativeToolResultReader,
): Promise<string> {
  const result = await nativeToolOutcome({ readToolResult: reader }, request, callId)
  if (result.failed === true || (result.exitCode !== undefined && result.exitCode !== 0))
    throw new Error('The exact native file Read returned a failure.')
  if (!expected || !excluded || expected === excluded || !result.text.includes(expected) || result.text.includes(excluded))
    throw new Error('The exact native file Read did not return the expected current bytes.')
  return result.text
}

export interface ShellToolExecutionOptions extends ToolPreparation {
  includeFailure?: boolean
  /**
   * Hold each command until the browser shows its output.
   *
   * A native shell tool can lose the output of a command that exits right after it
   * writes (see `OutputGate`). A held command stays alive until the live view shows
   * its output, so the tool cannot lose it. Only a provider with that defect sets
   * this option, because the hold changes the command that the model asks for.
   */
  outputGate?: boolean
}

/** Verify actual shell output and a failed command through the native tool path. */
export async function exerciseShellToolExecution(
  context: ManagedNativeScenarioContext,
  options: ShellToolExecutionOptions = {},
): Promise<void> {
  await options.prepare?.()
  const agent = await currentNativeAgent(context)
  const directory = createNativeToolDirectory(agent.workingDir)
  const outputFile = join(directory, 'native shell output.txt')
  expect(existsSync(outputFile)).toBe(false)
  const marker = uniqueMarker()
  const commands = [
    { command: `${printfMarkerCommand(`SHELL${marker}`, 42)} > ${quotePosixShellArgument(outputFile)}; cat ${quotePosixShellArgument(outputFile)}`, output: `SHELL${marker}42`, failed: false },
    ...(options.includeFailure === false ? [] : [{ command: `${printfMarkerCommand(`SHELLERR${marker}`, 77)} >&2; exit 7`, output: `SHELLERR${marker}77`, failed: true }]),
  ]
  for (const [index, command] of commands.entries()) {
    const answer = `The shell scenario ${index} ended.`
    const callId = `shell-${marker}-${index}`
    const outputRow = () => messageContents(context.page).filter({ hasText: command.output }).first()
    // The gate file lives in the literal private tool directory. A hold that quotes its path incorrectly runs the marker command, and the check of `command-expanded-marker` below finds it.
    const gate = options.outputGate ? createOutputGate(directory) : undefined
    const stepIndex = await context.modelScript.queue(
      { toolCalls: [bashToolCall(context.provider, callId, gate ? gate.hold(command.command) : command.command)] },
      nativeTextStep(context, answer),
    )
    await sendMessage(context.page, context.modelScript.prompt(`Run the native shell scenario ${index}.`))
    await runWithGatedOutput(
      gate && { gate, shown: () => expect(outputRow(), 'the live view shows the output of the held command').toBeVisible() },
      () => waitForNativeToolSteps(context, stepIndex + 2),
    )
    const request = await context.modelScript.requestAt(stepIndex + 1)
    const result = await nativeToolOutcome(context, request, callId)
    expect(result.text).toContain(command.output)
    if (!command.failed) {
      expect(readFileSync(outputFile, 'utf8')).toBe(`${command.output}\n`)
      expect(existsSync(join(agent.workingDir, 'command-expanded-marker'))).toBe(false)
    }
    await expect(outputRow()).toBeVisible()
    if (command.failed) {
      if ('exitCode' in result)
        expect(result.exitCode).toBe(7)
      else
        expect(result.text).toMatch(/(?:exit(?:ed)?(?: with)?[ _]code|exitCode|exit[ _]status)[^\d-]*7\b/i)
      await expect(messageBubbles(context.page).filter({ hasText: command.output }).first()).toContainText(/Error|failed|exit[^\d-]*7\b/i)
    }
    await expect(assistantBubbles(context.page).filter({ hasText: answer }).first()).toBeVisible()
  }
}

interface FileToolOptions extends ToolPreparation {
  /**
   * The tool step that changes the line `before` of the file at `path` to `after`, with the call ID `callId`. The
   * default is one edit call of the provider. A provider that edits through another tool, or that needs a capture from
   * its own request, states the step.
   */
  editStep?: (callId: string, path: string, before: string, after: string) => NativeToolStep
  readAfterCall?: (callId: string, path: string) => MockModelToolCall
}

interface FileSequenceOptions {
  workingDir: string
  fileName: string
}

export interface NativeFileSequence {
  filePath: string
  prompt: string
  /** The tool steps of the sequence, in order. */
  steps: MockModelStep[]
  /** The final answer. The caller queues it through `nativeTextStep`, so a provider that answers through a tool keeps its own form. */
  answer: string
}

/** Restrict a sequence to one file inside the supplied private working directory. */
function sequenceFilePath(options: FileSequenceOptions): string {
  if (!isAbsolute(options.workingDir))
    throw new Error('The native file sequence requires an absolute private working directory.')
  if (!isFileNameComponent(options.fileName))
    throw new Error('The native file sequence requires one filename component.')
  return join(options.workingDir, options.fileName)
}

/** The line that the file edit sequence seeds. The sequence replaces it with {@link PARITY_AFTER}. */
export const PARITY_BEFORE = 'const parityBefore = 1'

/** The line that the file edit sequence writes in place of {@link PARITY_BEFORE}. */
export const PARITY_AFTER = 'const parityAfter = 2'

/** Build the native edit sequence. Shell creates the file. Read and Edit use the same absolute path. Keep the fixed markers. */
export function nativeFileEditSequence(provider: NativeScenarioContext['provider'], options: FileSequenceOptions): NativeFileSequence {
  const filePath = sequenceFilePath(options)
  return {
    filePath,
    prompt: 'Create the file, read it, then change parityBefore to parityAfter.',
    steps: [
      { toolCalls: [bashToolCall(provider, 'seed-file', `printf "${PARITY_BEFORE}\\n" > ${quotePosixShellArgument(filePath)}`)] },
      { toolCalls: [readToolCall(provider, 'read-file', filePath)] },
      { toolCalls: [editToolCall(provider, 'edit-file', { path: filePath, before: PARITY_BEFORE, after: PARITY_AFTER })] },
    ],
    answer: 'The file is edited.',
  }
}

/**
 * Require one visible file diff in a chat message that shows both `before` and `after`.
 * The diff of an edit shows the removed and the added line. Two separate filters could match two different diffs,
 * such as a write diff that holds only the old line. A file tab draws a diff too, so the check reads the messages
 * alone.
 */
export async function expectFileDiff(page: Page, change: { before: string, after: string }): Promise<void> {
  if (!change.before || !change.after || change.before.includes(change.after) || change.after.includes(change.before)) {
    throw new Error('A file diff check needs two nonempty lines, and neither line may hold the other. An empty line '
      + 'matches every diff, and a line that holds the other matches a diff that shows only the longer line.')
  }
  const diff = messageBubbles(page).locator('[data-file-diff]:visible').filter({ hasText: change.after }).filter({ hasText: change.before })
  await expect(diff.first(), 'one visible file diff shows the old and the new line').toBeVisible()
}

/** Retain the original native Write turn and its fixed file marker. */
export function nativeFileWriteSequence(provider: NativeScenarioContext['provider'], options: FileSequenceOptions): NativeFileSequence {
  const filePath = sequenceFilePath(options)
  return {
    filePath,
    prompt: `Write ${options.fileName} with one marker line.`,
    steps: [
      { toolCalls: [writeToolCall(provider, 'write-file', { path: filePath, content: 'written-42\n' })] },
    ],
    answer: 'The file is written.',
  }
}

/**
 * Complete the original edit sequence and preserve its visible diff and disk guards.
 * Return the step index of the seed step, the first step of the sequence. The request at `start + n` holds the result
 * of the step at `start + n - 1`.
 */
export async function exerciseFileEditSequence(
  context: NativeScenarioContext,
  options: FileSequenceOptions & { approveSeed?: (firstStepCount: number) => Promise<void> },
): Promise<number> {
  const sequence = nativeFileEditSequence(context.provider, options)
  const start = await context.modelScript.queue(...sequence.steps, nativeTextStep(context, sequence.answer))
  await sendMessage(context.page, context.modelScript.prompt(sequence.prompt))
  await options.approveSeed?.(start + 1)
  await context.modelScript.waitForSteps(start + sequence.steps.length + 1)
  await waitForAgentIdle(context.page)
  await expectFileDiff(context.page, { before: PARITY_BEFORE, after: PARITY_AFTER })
  const onDisk = readFileSync(sequence.filePath, 'utf8')
  expect(onDisk, 'the edit changed the file on disk').toContain(PARITY_AFTER)
  expect(onDisk, 'the edit replaced the seeded line').not.toContain(PARITY_BEFORE)
  return start
}

/** Complete the original Write sequence and preserve its actual disk result. */
export async function exerciseFileWriteSequence(context: NativeScenarioContext, options: FileSequenceOptions): Promise<void> {
  const sequence = nativeFileWriteSequence(context.provider, options)
  const start = await context.modelScript.queue(...sequence.steps, nativeTextStep(context, sequence.answer))
  await sendMessage(context.page, context.modelScript.prompt(sequence.prompt))
  await context.modelScript.waitForSteps(start + sequence.steps.length + 1)
  await waitForAgentIdle(context.page)
  expect(readFileSync(sequence.filePath, 'utf8'), 'the write changed the file on disk').toContain('written-42')
}

/** Verify native reads, the applied edit diff, and real file creation. */
export async function exerciseFileToolExecution(
  context: ManagedNativeScenarioContext,
  options: FileToolOptions = {},
): Promise<void> {
  await options.prepare?.()
  const agent = await currentNativeAgent(context)
  if (!agent.workingDir)
    throw new Error('The native file scenario requires a working directory.')
  const marker = uniqueMarker()
  const directory = createNativeToolDirectory(agent.workingDir)
  const file = join(directory, `native-file-${marker}.txt`)
  const created = join(directory, `native-created-${marker}.txt`)
  const before = `OLD${marker}`
  const after = `NEW${marker}`
  const written = `CREATED${marker}\n`
  writeFileSync(file, `${before}\n`)
  expect(existsSync(created)).toBe(false)
  const stepIndex = await runNativeToolSteps(context, {
    steps: [
      { toolCalls: [readToolCall(context.provider, 'native-read-before', file)] },
      options.editStep?.('native-edit', file, before, after)
      ?? { toolCalls: [editToolCall(context.provider, 'native-edit', { path: file, before, after })] },
      { toolCalls: [options.readAfterCall?.('native-read-after', file) ?? readToolCall(context.provider, 'native-read-after', file)] },
      { toolCalls: [writeToolCall(context.provider, 'native-write', { path: created, content: written })] },
    ],
    prompt: 'Read the scratch file, edit it, read it again, and create the second file.',
    answer: 'The native file operations ended.',
  })
  await nativeFileReadResult(await context.modelScript.requestAt(stepIndex + 1), 'native-read-before', before, after, context.readToolResult)
  await nativeFileReadResult(await context.modelScript.requestAt(stepIndex + 3), 'native-read-after', after, before, context.readToolResult)
  expect(readFileSync(file, 'utf8')).toBe(`${after}\n`)
  expect(readFileSync(created, 'utf8')).toBe(written)
  await expectFileDiff(context.page, { before, after })
  await context.page.reload()
  await expectFileDiff(context.page, { before, after })
}
