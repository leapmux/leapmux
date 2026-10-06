import type { Page } from '@playwright/test'
import type { MockModelRequestRecord, MockModelStep, MockModelToolCall } from './mockModelScript'
import type { ModelScript } from './modelScriptFixture'
import type { ManagedNativeScenarioContext, NativeScenarioContext, NativeToolResultReader } from './nativeScenario'
import { randomUUID } from 'node:crypto'
import { existsSync, readFileSync, writeFileSync } from 'node:fs'
import { basename, isAbsolute, join } from 'node:path'
import { expect } from '@playwright/test'
import { currentNativeAgent, nativeTextStep, nativeToolOutcome } from './nativeScenario'
import { createNativeToolDirectory } from './nativeToolDirectory'
import { nativeToolResult } from './nativeToolResult'
import { createOutputGate, runWithGatedOutput } from './outputGate'
import { bashToolCall, editToolCall, readToolCall, writeToolCall } from './providerToolCalls'
import { printfMarkerCommand, quotePosixShellArgument } from './shellArguments'
import { assistantBubbles, controlButton, messageBubbles, messageContents, sendMessage, waitForAgentIdle } from './ui'

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

/** Allow only actual native tool approval requests until the script completes. */
export async function waitForNativeToolSteps(context: NativeScenarioContext, target: number, options: { beforeIdle?: () => Promise<void> } = {}): Promise<void> {
  // `clickNativeToolApproval` checks and clicks one button in one browser operation, so the loop reads the first visible Allow button.
  const allow = controlButton(context.page, 'allow').first()
  let approvals = 0
  while ((await context.modelScript.status()).nextStep < target) {
    await expect.poll(async () => {
      return processNativeToolApproval({
        completed: async () => (await context.modelScript.status()).nextStep >= target,
        clickIfReady: async () => {
          const clicked = await allow.evaluateAll(clickNativeToolApproval, approvals < 16)
          if (clicked)
            approvals++
          return clicked
        },
      })
    }).not.toBe('waiting')
  }
  await context.modelScript.waitForSteps(target)
  await options.beforeIdle?.()
  await waitForAgentIdle(context.page)
}

/** One native turn: a model step that calls tools, then a model step that answers. */
export interface NativeToolTurn {
  toolCalls: readonly MockModelToolCall[]
  /** Text that the tool step captures from its own request, for a `{{name}}` placeholder in a tool call. */
  captures?: Readonly<Record<string, string>>
  /** The prompt that starts the turn. The turn marks it for this test's script. */
  prompt: string
  /** The final answer. It goes through `nativeTextStep`, so a provider that answers through a tool keeps its own form. */
  answer: string
  /** Send the marked prompt. `sendMessage` by default; a turn that attaches a file passes its own sender. */
  send?: (page: Page, text: string) => Promise<void>
}

/** The requests of one {@link NativeToolTurn}. */
export interface NativeToolTurnRequests {
  /** The step index of the tool step. The answer step is `start + 1`. */
  start: number
  /** The request that the tool step answered. It holds the tool catalog that the native client offered. */
  toolRequest: MockModelRequestRecord
  /** The request that the answer step answered. It holds the results of the tool calls. */
  resultRequest: MockModelRequestRecord
}

/**
 * Run one native tool turn and return both of its model requests.
 * The turn allows each native approval through {@link waitForNativeToolSteps}, and it ends after the agent is idle.
 * A turn that answers a banner or a form between its two steps cannot use this helper.
 */
export async function runNativeToolTurn(context: NativeScenarioContext, turn: NativeToolTurn): Promise<NativeToolTurnRequests> {
  if (turn.toolCalls.length === 0)
    throw new Error('A native tool turn needs at least one tool call.')
  const toolStep: MockModelStep = { toolCalls: [...turn.toolCalls], ...(turn.captures ? { captures: { ...turn.captures } } : {}) }
  const start = await context.modelScript.queue(toolStep, nativeTextStep(context, turn.answer))
  await (turn.send ?? sendMessage)(context.page, context.modelScript.prompt(turn.prompt))
  await waitForNativeToolSteps(context, start + 2)
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
  const marker = randomUUID().replaceAll('-', '')
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
  writeCall?: (callId: string, path: string, content: string) => MockModelToolCall
  editCall?: (callId: string, path: string, before: string, after: string) => MockModelToolCall
  editStep?: (callId: string, path: string, before: string, after: string) => MockModelStep
  readAfterCall?: (callId: string, path: string) => MockModelToolCall
}

interface FileSequenceOptions {
  workingDir: string
  fileName: string
}

export interface NativeFileSequence {
  filePath: string
  prompt: string
  steps: MockModelStep[]
}

/** Restrict a sequence to one file inside the supplied private working directory. */
function sequenceFilePath(options: FileSequenceOptions): string {
  if (!isAbsolute(options.workingDir))
    throw new Error('The native file sequence requires an absolute private working directory.')
  if (!options.fileName || options.fileName === '.' || options.fileName === '..'
    || basename(options.fileName) !== options.fileName || options.fileName.includes('\\') || options.fileName.includes('\0')) {
    throw new Error('The native file sequence requires one filename component.')
  }
  return join(options.workingDir, options.fileName)
}

/** Build the native edit sequence. Shell creates the file. Read and Edit use the same absolute path. Keep the fixed markers. */
export function nativeFileEditSequence(provider: NativeScenarioContext['provider'], options: FileSequenceOptions): NativeFileSequence {
  const filePath = sequenceFilePath(options)
  return {
    filePath,
    prompt: 'Create the file, read it, then change parityBefore to parityAfter.',
    steps: [
      { toolCalls: [bashToolCall(provider, 'seed-file', `printf "const parityBefore = 1\\n" > ${quotePosixShellArgument(filePath)}`)] },
      { toolCalls: [readToolCall(provider, 'read-file', filePath)] },
      { toolCalls: [editToolCall(provider, 'edit-file', { path: filePath, before: 'const parityBefore = 1', after: 'const parityAfter = 2' })] },
      { text: 'The file is edited.' },
    ],
  }
}

/** Retain the original native Write turn and its fixed file marker. */
export function nativeFileWriteSequence(provider: NativeScenarioContext['provider'], options: FileSequenceOptions): NativeFileSequence {
  const filePath = sequenceFilePath(options)
  return {
    filePath,
    prompt: `Write ${options.fileName} with one marker line.`,
    steps: [
      { toolCalls: [writeToolCall(provider, 'write-file', { path: filePath, content: 'written-42\n' })] },
      { text: 'The file is written.' },
    ],
  }
}

/** Complete the original edit sequence and preserve its visible diff and disk guards. */
export async function exerciseFileEditSequence(
  context: NativeScenarioContext,
  options: FileSequenceOptions & { approveSeed?: (firstStepCount: number) => Promise<void> },
): Promise<void> {
  const sequence = nativeFileEditSequence(context.provider, options)
  const start = await context.modelScript.queue(...sequence.steps)
  await sendMessage(context.page, context.modelScript.prompt(sequence.prompt))
  await options.approveSeed?.(start + 1)
  await context.modelScript.waitForSteps(start + sequence.steps.length)
  await waitForAgentIdle(context.page)
  const diff = context.page.locator('[data-file-diff]:visible')
  await expect(diff.filter({ hasText: 'const parityAfter = 2' }).first()).toBeVisible()
  await expect(diff.filter({ hasText: 'const parityBefore = 1' }).first()).toBeVisible()
  const onDisk = readFileSync(sequence.filePath, 'utf8')
  expect(onDisk, 'the edit changed the file on disk').toContain('const parityAfter = 2')
  expect(onDisk, 'the edit replaced the seeded line').not.toContain('const parityBefore = 1')
}

/** Complete the original Write sequence and preserve its actual disk result. */
export async function exerciseFileWriteSequence(context: NativeScenarioContext, options: FileSequenceOptions): Promise<void> {
  const sequence = nativeFileWriteSequence(context.provider, options)
  const start = await context.modelScript.queue(...sequence.steps)
  await sendMessage(context.page, context.modelScript.prompt(sequence.prompt))
  await context.modelScript.waitForSteps(start + sequence.steps.length)
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
  const marker = randomUUID().replaceAll('-', '')
  const directory = createNativeToolDirectory(agent.workingDir)
  const file = join(directory, `native-file-${marker}.txt`)
  const created = join(directory, `native-created-${marker}.txt`)
  const before = `OLD${marker}`
  const after = `NEW${marker}`
  const written = `CREATED${marker}\n`
  writeFileSync(file, `${before}\n`)
  expect(existsSync(created)).toBe(false)
  const stepIndex = await context.modelScript.queue(
    { toolCalls: [readToolCall(context.provider, 'native-read-before', file)] },
    options.editStep?.('native-edit', file, before, after)
    ?? { toolCalls: [options.editCall?.('native-edit', file, before, after) ?? editToolCall(context.provider, 'native-edit', { path: file, before, after })] },
    { toolCalls: [options.readAfterCall?.('native-read-after', file) ?? readToolCall(context.provider, 'native-read-after', file)] },
    { toolCalls: [options.writeCall?.('native-write', created, written) ?? writeToolCall(context.provider, 'native-write', { path: created, content: written })] },
    nativeTextStep(context, 'The native file operations ended.'),
  )
  await sendMessage(context.page, context.modelScript.prompt('Read the scratch file, edit it, read it again, and create the second file.'))
  await waitForNativeToolSteps(context, stepIndex + 5)
  await nativeFileReadResult(await context.modelScript.requestAt(stepIndex + 1), 'native-read-before', before, after, context.readToolResult)
  await nativeFileReadResult(await context.modelScript.requestAt(stepIndex + 3), 'native-read-after', after, before, context.readToolResult)
  expect(readFileSync(file, 'utf8')).toBe(`${after}\n`)
  expect(readFileSync(created, 'utf8')).toBe(written)
  const diff = messageBubbles(context.page).locator('[data-file-diff]').filter({ hasText: after }).first()
  await expect(diff).toBeVisible()
  await expect(diff).toContainText(before)
  await context.page.reload()
  await expect(messageBubbles(context.page).locator('[data-file-diff]').filter({ hasText: after }).first()).toBeVisible()
}
