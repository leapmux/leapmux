import type { Locator, Page } from '@playwright/test'
import type { MockModelRequestRecord, MockModelStep, MockModelToolCall } from './mockModelScript'
import type { ModelScript } from './modelScriptFixture'
import type { ManagedNativeScenarioContext, NativeScenarioContext, NativeToolOutcome, NativeToolResultReader } from './nativeScenario'
import type { GatedOutput } from './outputGate'
import { existsSync, readFileSync, writeFileSync } from 'node:fs'
import { isAbsolute, join } from 'node:path'
import { expect } from '@playwright/test'
import { toolOutcomeLabel } from '../../../src/components/chat/results/toolOutcomeLabel'
import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { currentNativeAgent, nativeTextStep, nativeToolOutcome } from './nativeScenario'
import { createNativeToolDirectory } from './nativeToolDirectory'
import { nativeToolResult } from './nativeToolResult'
import { createOutputGate, runWithGatedOutput } from './outputGate'
import { bashToolCall, editToolCall, readToolCall, writeToolCall } from './providerToolCalls'
import { isFileNameComponent } from './runDirectory'
import { printfMarkerCommand, quotePosixShellArgument, uniqueMarker } from './shellArguments'
import { assistantBubbles, chatText, controlButton, expectNoControlBanner, messageBubbles, railedRows, sendMessage, toolRows, waitForAgentIdle } from './ui'

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

/** How {@link approveNativeToolsUntil} finds and allows a request. Each field has a default. */
export interface NativeApprovalOptions {
  /**
   * The Allow button. The default is the first visible Allow button of the page: `clickNativeToolApproval` checks and
   * clicks one button in one browser operation, so the loop reads one button. A page that shows more than one agent
   * passes a locator that is scoped to the agent.
   */
  allow?: Locator
  /**
   * Decide before each click whether the request on the page is the one to allow. It receives the count of approvals
   * so far. False waits and reads again, and a thrown error fails the wait. A provider proves its own facts here, such
   * as the native session and the request frame. The default allows each request.
   */
  ready?: (approvals: number) => Promise<boolean>
}

/**
 * Allow each actual native approval request until `completed` reports true. `completed` receives the count of
 * approvals so far, so it can refuse an operation that completed with no approval. A thrown error of either callback
 * fails the wait.
 * The wait fails when the agent asks for more than {@link NATIVE_APPROVAL_LIMIT} approvals.
 */
export async function approveNativeToolsUntil(page: Page, completed: (approvals: number) => Promise<boolean>, options: NativeApprovalOptions = {}): Promise<void> {
  const allow = options.allow ?? controlButton(page, 'allow').first()
  let approvals = 0
  while (!await completed(approvals)) {
    await expect.poll(async () => {
      return processNativeToolApproval({
        completed: () => completed(approvals),
        clickIfReady: async () => {
          if (options.ready && !await options.ready(approvals))
            return false
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
  /** The thinking that the model reports before the tool calls of this step. */
  reasoning?: string
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
  return {
    ...(step.reasoning !== undefined ? { reasoning: step.reasoning } : {}),
    toolCalls: [...step.toolCalls],
    ...(step.captures ? { captures: { ...step.captures } } : {}),
  }
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
  const { toolCalls, reasoning, captures, ...rest } = turn
  const start = await runNativeToolSteps(context, { ...rest, steps: [{ toolCalls, ...(reasoning !== undefined ? { reasoning } : {}), ...(captures ? { captures } : {}) }] })
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

/**
 * A part of the shared shell row that a provider does not draw:
 *
 * - `outputFilePath`: a small output shows no output-path chip.
 */
type ShellRowPart = 'outputFilePath'

/**
 * The parts of the shared shell row that a provider does not draw, each with the reason. A part belongs to the row of
 * the provider, so every scenario that runs a shell command of the provider reads the same entry. A provider that the
 * table does not list draws every part.
 */
const SHELL_ROW_EXCEPTIONS: Readonly<Partial<Record<AgentProvider, Readonly<Partial<Record<ShellRowPart, string>>>>>> = {
  [AgentProvider.GROK_BUILD]: {
    outputFilePath: 'Grok Build keeps the output of every shell command in a log file, and states its path as '
      + '`rawOutput.output_file` also for a small output that it did not truncate. The row shows that path for every '
      + 'command, as the Grok Build cell of output-file-paths states.',
  },
}

/**
 * The providers that run one tool call of a model answer, each with the evidence. The shell scenario gives each of
 * their commands a model answer of its own, still in one turn; every other provider gets both commands in one answer.
 */
const ONE_SHELL_CALL_PER_ANSWER: ReadonlyMap<AgentProvider, string> = new Map([
  [AgentProvider.JUNIE, 'Junie 26.9.22 runs the first tool call of a model answer only: its next request replays that '
  + 'call alone in the assistant message and holds no result for the second call.'],
])

/** One shell command of a scenario, as its rows must show it. */
export interface ShellRowCommand {
  /** The text that the command prints. No scripted prompt or answer holds it. */
  output: string
  /**
   * The text that the command passes to `printf`, which `output` holds with the computed digits after it. The command
   * holds it also, so a row that holds `printf` and this text shows the command, not only its output.
   */
  printedPrefix: string
  /** The code that the command exits with. */
  exitCode: number
  /** Require this exact marked output after the helper expands the result row. */
  exactOutput?: string
  /** Native notice fragments that this result body must omit. */
  absentRowText?: readonly string[]
}

/** The generated command and the output that its native result must prove. */
export interface ShellCommand extends ShellRowCommand {
  callId: string
  command: string
}

/** A native result proves its output, or a provider proves an exact failure record. */
export type ShellResultEvidence
  = { kind: 'output', outcome: NativeToolOutcome, absentRowText?: readonly string[] }
    | { kind: 'record', outcome: NativeToolOutcome & { failed: true, exitCode: number }, record: string }

/** What the rows of the shell commands of one provider must not draw. */
export interface ShellRowOptions {
  /**
   * Fixed parts of the notice, trailer or record that the provider adds to a command result for its model, and that
   * no row may draw. The check reads the whole text of each output row, so it expands a collapsed row first: a
   * collapsed result shows its first lines only, and a trailer is often the last line.
   */
  absentRowText?: readonly string[]
}

export interface ShellToolExecutionOptions extends ToolPreparation, ShellRowOptions {
  /** Read a provider's native result and decide which exact evidence its row must preserve. */
  readResult?: (request: MockModelRequestRecord, command: Readonly<ShellCommand>) => ShellResultEvidence | Promise<ShellResultEvidence>
  /** Run the failed command beside the successful one. The default is true. */
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
  /** Reload the page after the turn, and prove every row again from the stored transcript. */
  reload?: boolean
}

/** The visible result rows of tool calls. */
function toolResultBubbles(page: Page): Locator {
  return page.locator('[data-testid="message-bubble"][data-tool-row-role="result"]:visible')
}

/** The visible rows of tool calls, whatever their role. */
function toolCallBubbles(page: Page): Locator {
  return page.locator('[data-testid="message-bubble"][data-tool-row-role]:visible')
}

/** The name of the control that expands a collapsed result: the outer toolbar and the inner header word it apart. */
const EXPAND_RESULT = /^Expand(?: output)?$/

/** Expand each collapsed row of `rows`, so the page holds its whole text. A row that shows all of its text has no such control. */
async function expandCollapsedRows(rows: Locator): Promise<void> {
  for (const row of await rows.all()) {
    const view = row.locator('..')
    await view.hover()
    const expand = view.getByRole('button', { name: EXPAND_RESULT })
    if (await expand.count() === 0)
      continue
    await expand.first().click()
    await expect(view.getByRole('button', { name: 'Collapse', exact: true }).first(), 'the expanded row offers to collapse again').toBeVisible()
  }
}

/**
 * Require the rows that a provider draws for its shell commands:
 *
 * - A tool row draws the output, and a tool row shows the command.
 * - A row of the call draws the rail of its span.
 * - The result row reaches its final status, and the header of a failed row states its exit code.
 * - A small output shows no output path.
 * - Each command keeps its own rows, also when one model answer called both.
 * - No row draws a text of `absentRowText`.
 *
 * {@link SHELL_ROW_EXCEPTIONS} states the parts that a provider does not draw.
 */
export async function expectShellToolRows(
  context: Pick<NativeScenarioContext, 'page' | 'provider'>,
  commands: readonly ShellRowCommand[],
  options: ShellRowOptions = {},
): Promise<void> {
  const { page } = context
  const exceptions = SHELL_ROW_EXCEPTIONS[context.provider] ?? {}
  for (const command of commands) {
    if (command.exactOutput !== undefined) {
      const selected = toolResultBubbles(page).filter({ hasText: command.printedPrefix })
      await expect(selected, 'the native record identifies one result row').toHaveCount(1)
      await expandCollapsedRows(selected)
      const preview = selected.locator('[data-tool-output-preview]')
      await expect(preview, 'the native record has one marked output').toHaveCount(1)
      expect(await preview.textContent(), 'the row preserves the exact native record').toBe(command.exactOutput)
    }
    const outputRows = toolRows(page).filter({ hasText: command.output })
    const resultRows = toolResultBubbles(page).filter({ hasText: command.output })
    await expect(outputRows.first(), 'a tool row draws the output of the command').toBeVisible()
    await expect(toolRows(page).filter({ hasText: 'printf' }).filter({ hasText: command.printedPrefix }).first(), 'a tool row shows the command').toBeVisible()
    await expect(railedRows(page).filter({ hasText: command.output }).first(), 'the output row draws the rail of its span').toBeVisible()
    await expect(resultRows.first(), 'the result row of the command reaches its final status')
      .toHaveAttribute('data-tool-status', command.exitCode === 0 ? 'completed' : /^(?:completed|failed)$/)
    if (command.exitCode !== 0) {
      await expect(outputRows.filter({ hasText: toolOutcomeLabel('failed', `exit ${command.exitCode}`) }).first(), 'the header of the failed row states the exit code')
        .toBeVisible()
    }
    if (exceptions.outputFilePath === undefined)
      await expect(resultRows.getByTestId('tool-output-file-paths'), 'a small output shows no output path').toHaveCount(0)
    if (command.absentRowText?.length) {
      await expandCollapsedRows(resultRows)
      const text = (await resultRows.allTextContents()).join('\n')
      for (const absent of command.absentRowText) {
        if (!absent.trim())
          throw new Error('An absent row text must contain a nonblank native notice fragment.')
        expect(text, 'the result body omits its native notice').not.toContain(absent)
      }
    }
  }
  for (const [index, first] of commands.entries()) {
    for (const second of commands.slice(index + 1)) {
      await expect(toolCallBubbles(page).filter({ hasText: first.printedPrefix }).filter({ hasText: second.printedPrefix }), 'each command keeps its own rows')
        .toHaveCount(0)
    }
  }
  if (!options.absentRowText?.length)
    return
  for (const command of commands)
    await expandCollapsedRows(toolResultBubbles(page).filter({ hasText: command.output }))
  const text = await chatText(page)
  for (const absent of options.absentRowText) {
    if (!absent.trim())
      throw new Error('An absent row text must hold a nonblank fixed part of the native notice.')
    expect(text, `no row draws the native text ${JSON.stringify(absent)}`).not.toContain(absent)
  }
}

/** Run `run` while each gate holds its command, and release each gate after the browser shows the output of its own command. */
function runWithGatedOutputs<T>(gated: readonly GatedOutput[], run: () => Promise<T>): Promise<T> {
  return gated.reduceRight<() => Promise<T>>((inner, each) => () => runWithGatedOutput(each, inner), run)()
}

/**
 * Run a successful and a failed shell command through the provider's native shell tool, both in ONE model answer, and
 * prove the native results and the rows. A provider of {@link ONE_SHELL_CALL_PER_ANSWER} gets one answer for each
 * command, in the same turn.
 *
 * - The output of each command reaches the next model request, and the successful command wrote its file in the literal
 *   private directory, whose name holds shell metacharacters.
 * - The nonzero exit reaches the model, or the Worker frame where the provider's reader reads it.
 * - {@link expectShellToolRows} proves the rows, again after a reload when `reload` is set.
 *
 * One answer that calls both commands proves that each call keeps its own rows. A scenario of one command
 * (`includeFailure: false`) calls one.
 */
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
    { callId: `shell-${marker}-0`, command: `${printfMarkerCommand(`SHELL${marker}`, 42)} > ${quotePosixShellArgument(outputFile)}; cat ${quotePosixShellArgument(outputFile)}`, printedPrefix: `SHELL${marker}`, output: `SHELL${marker}42`, exitCode: 0 },
    ...(options.includeFailure === false ? [] : [{ callId: `shell-${marker}-1`, command: `${printfMarkerCommand(`SHELLERR${marker}`, 77)} >&2; exit 7`, printedPrefix: `SHELLERR${marker}`, output: `SHELLERR${marker}77`, exitCode: 7 }]),
  ]
  // The gate files live in the literal private tool directory. A hold that quotes its path incorrectly runs the marker
  // command, and the check of `command-expanded-marker` below finds it. Each command takes its own gate, so a provider
  // that runs the two calls one after the other releases the first before the second starts.
  const gates = commands.map(() => options.outputGate ? createOutputGate(directory) : undefined)
  const calls = commands.map((command, index) => bashToolCall(context.provider, command.callId, gates[index]?.hold(command.command) ?? command.command))
  const callPerAnswer = ONE_SHELL_CALL_PER_ANSWER.has(context.provider)
  const steps: MockModelStep[] = callPerAnswer ? calls.map(call => ({ toolCalls: [call] })) : [{ toolCalls: calls }]
  const answer = 'The native shell scenario ended.'
  const stepIndex = await context.modelScript.queue(...steps, nativeTextStep(context, answer))
  await sendMessage(context.page, context.modelScript.prompt('Run the native shell scenario.'))
  const gated = commands.flatMap((command, index) => {
    const gate = gates[index]
    return gate ? [{ gate, shown: () => expect(toolRows(context.page).filter({ hasText: command.output }).first(), 'the live view shows the output of the held command').toBeVisible() }] : []
  })
  await runWithGatedOutputs(gated, () => waitForNativeToolSteps(context, stepIndex + steps.length + 1))
  const rowCommands: ShellRowCommand[] = []
  for (const [index, command] of commands.entries()) {
    // The request after a tool step holds the results of that step.
    const request = await context.modelScript.requestAt(stepIndex + (callPerAnswer ? index : 0) + 1)
    const evidence: ShellResultEvidence = options.readResult
      ? await options.readResult(request, command)
      : { kind: 'output' as const, outcome: await nativeToolOutcome(context, request, command.callId) }
    const result = evidence.outcome
    if (evidence.kind === 'record') {
      if (command.exitCode === 0 || result.failed !== true || result.exitCode === undefined || result.exitCode === 0
        || !evidence.record.trim() || evidence.record !== result.text) {
        throw new Error('A native record must preserve an exact nonempty failed result with a nonzero exit code.')
      }
      rowCommands.push({ ...command, output: evidence.record, exactOutput: evidence.record })
    }
    else {
      expect(result.text, 'the next model request holds the output of the command').toContain(command.output)
      rowCommands.push({ ...command, ...(evidence.absentRowText === undefined ? {} : { absentRowText: evidence.absentRowText }) })
    }
    if (command.exitCode === 0) {
      expect(readFileSync(outputFile, 'utf8')).toBe(`${command.output}\n`)
      expect(existsSync(join(agent.workingDir, 'command-expanded-marker'))).toBe(false)
    }
    else if ('exitCode' in result) {
      expect(result.exitCode, 'the native exit code reaches LeapMux').toBe(command.exitCode)
    }
    else {
      expect(result.text, 'the next model request states the native exit code').toMatch(new RegExp(`(?:exit(?:ed)?(?: with)?[ _]code|exitCode|exit[ _]status)[^\\d-]*${command.exitCode}\\b`, 'i'))
    }
  }
  await expect(assistantBubbles(context.page).filter({ hasText: answer }).first()).toBeVisible()
  await expectShellToolRows(context, rowCommands, options)
  if (!options.reload)
    return
  await context.page.reload()
  await expect(assistantBubbles(context.page).filter({ hasText: answer }).first(), 'the answer of the turn stays after a reload').toBeVisible()
  await expectShellToolRows(context, rowCommands, options)
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

/** The file name of the native read, edit, and write scenarios below. Each scenario writes it in its own directory. */
const NATIVE_FILE_NAMES = { read: 'notes.txt', edit: 'parity.ts', write: 'note.txt' } as const

/**
 * Read three seeded lines through the provider's native read tool, and require the row to draw the lines of the file:
 * the last line shows, and the numbered form that the native tool returns to the model does not. `numberedLine`
 * states that form for the provider, such as `3: text`.
 */
export async function exerciseNativeFileRead(
  context: NativeScenarioContext,
  options: { directory: string, linePrefix: string, numberedLine: (lineNumber: number, text: string) => string },
): Promise<void> {
  if (!options.linePrefix)
    throw new Error('The native read needs a line prefix that only the file holds.')
  const notes = join(options.directory, NATIVE_FILE_NAMES.read)
  const lines = [1, 2, 3].map(index => `${options.linePrefix}-${index}`)
  writeFileSync(notes, `${lines.join('\n')}\n`)
  await runNativeToolTurn(context, {
    toolCalls: [readToolCall(context.provider, 'read-notes', notes)],
    prompt: 'Read the notes back.',
    answer: 'I read the notes.',
  })
  await expect.poll(() => chatText(context.page), 'the read row draws the last line of the file').toContain(lines[2])
  expect(await chatText(context.page), 'the read row draws the lines of the file, not their numbered form').not.toContain(options.numberedLine(3, lines[2]!))
}

/**
 * Change one seeded line through the provider's native edit tool, and require the exact bytes on disk and the diff of
 * the edit in the transcript.
 */
export async function exerciseNativeFileEdit(context: NativeScenarioContext, options: { directory: string }): Promise<void> {
  const path = join(options.directory, NATIVE_FILE_NAMES.edit)
  writeFileSync(path, `${PARITY_BEFORE}\n`)
  await runNativeToolTurn(context, {
    toolCalls: [editToolCall(context.provider, 'parity-edit', { path, before: PARITY_BEFORE, after: PARITY_AFTER })],
    prompt: 'Change parity.ts.',
    answer: 'I changed parity.ts.',
  })
  expect(readFileSync(path, 'utf8'), 'the edit stores exactly the changed line').toBe(`${PARITY_AFTER}\n`)
  await expectFileDiff(context.page, { before: PARITY_BEFORE, after: PARITY_AFTER })
}

/**
 * Create a missing file through the provider's native write tool, and require the stored bytes and the file name in
 * the transcript. `stored` states the bytes that the provider stores for `content`: the same text by default, a
 * string for exact other bytes (Amp appends a final newline), or a pattern for a provider whose exact bytes the suite
 * has not pinned. Give a pattern anchored at both ends, so it states the whole file.
 */
export async function exerciseNativeFileWrite(
  context: NativeScenarioContext,
  options: { directory: string, content: string, stored?: string | RegExp },
): Promise<void> {
  if (!options.content)
    throw new Error('The native write needs content.')
  const stored = options.stored ?? options.content
  if (stored instanceof RegExp && (!stored.source.startsWith('^') || !stored.source.endsWith('$')))
    throw new Error('A stored-bytes pattern must state the whole file, from ^ to $.')
  const path = join(options.directory, NATIVE_FILE_NAMES.write)
  expect(existsSync(path), 'the written file does not exist before the write').toBe(false)
  await runNativeToolTurn(context, {
    toolCalls: [writeToolCall(context.provider, 'write-call', { path, content: options.content })],
    prompt: 'Write the note.',
    answer: 'I wrote the note.',
  })
  const bytes = readFileSync(path, 'utf8')
  if (typeof stored === 'string')
    expect(bytes, 'the write stores exactly the stated bytes').toBe(stored)
  else
    expect(bytes, 'the write stores the stated bytes').toMatch(stored)
  await expect.poll(() => chatText(context.page), 'the transcript names the written file').toContain(NATIVE_FILE_NAMES.write)
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
