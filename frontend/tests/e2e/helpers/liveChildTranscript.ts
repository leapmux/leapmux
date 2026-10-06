import type { Locator } from '@playwright/test'
import type { MockModelMatcher, MockModelRule, MockModelStep } from './mockModelScript'
import type { ModelScript } from './modelScriptFixture'
import type { ManagedNativeScenarioContext } from './nativeScenario'
import type { HeldNativeChild } from './runningChildProof'
import { writeFileSync } from 'node:fs'
import { basename, join } from 'node:path'
import { expect } from '@playwright/test'
import { decompressContentToString } from '../../../src/lib/decompress'
import { markdownToPlainText } from '../../../src/lib/markdownPlainText'
import { finishCleanup, withCleanup } from './cleanup'
import { readAllAgentMessages } from './nativeMessages'
import { expandNativeResultView } from './nativeResultView'
import { selectedAgentTabId } from './nativeScenario'
import { nativeToolResult } from './nativeToolResult'
import { bashToolCall, readToolCall, spawnSubagentToolCall } from './providerToolCalls'
import { uniqueMarker } from './shellArguments'
import { expectNoRegistryRows, openChildTabFromRow, requireRegistryRow } from './subagentRegistry'
import { assistantBubbles, messageContents, sendMessage, tabById, toolCallRow, toolRows, userBubbles } from './ui'

/** The final child answer when the spec supplies no native final response. */
const DEFAULT_CHILD_ANSWER = 'CHILD_LIVE_DONE'

/** The call ID of the spawn call of the parent. A provider rule that answers for the spawn, such as a permission judge, names it. */
export const LIVE_CHILD_SPAWN_CALL_ID = 'spawn-live-child'

/** The call ID of the native Read that the helper scripts for the child. */
export const LIVE_CHILD_READ_CALL_ID = 'live-child-read'

/** The call ID of the native shell command that the helper scripts for the child. */
export const LIVE_CHILD_SHELL_CALL_ID = 'live-child-shell'

/** The fixtures that a live child needs. The provider decides the spawn call and the child tool calls. */
export type LiveChildContext = Pick<ManagedNativeScenarioContext, 'page' | 'modelScript' | 'leapmuxServer' | 'provider'>

/**
 * The tool that the child runs before its held final response.
 *
 * - `read`: the child reads a file with a computed marker. The marker must show in the child tab and never in the
 *   parent tab. `expandResult` expands the result view of that Read before the helper looks for the marker. A result
 *   view shows only its first `COLLAPSED_RESULT_ROWS` rows (`~/components/chat/results/collapse`) until the reader
 *   expands it. Set `expandResult` when the native Read result puts header rows before the file text, so the marker
 *   row is not in the page while the view is collapsed.
 * - `shell`: the child runs `command`. Its tool row must show in the child tab.
 */
export type LiveChildToolProof
  = | { read: { workingDir: string, expandResult?: boolean } }
    | { shell: { command: string } }

export interface LiveChildSpec {
  childWhen: MockModelMatcher
  /**
   * The task that the spawn call gives the child. The child tab shows it in a user bubble, which renders it as
   * Markdown, so the helper looks for its words without the Markdown syntax: the bubble of `` Run `echo x` `` shows
   * "Run echo x". The task must show one or more words.
   */
  childTask: string
  parentTask: string
  /**
   * The native final response of the child. The default is the text CHILD_LIVE_DONE.
   * The gate holds this response, with or without the tool of `toolProof`.
   * When the response holds text, the words of that text must stay out of the child tab while the gate holds it. An
   * assistant bubble renders the text as Markdown, as a user bubble does.
   */
  childResponse?: Omit<MockModelStep, 'gate'>
  /**
   * Require the text of the final response in the child tab after the release. The default is true.
   * Pass false for a provider whose child sends no text of its own, such as Goose and ZCode.
   */
  finalAnswerInChildTab?: boolean
  holdParentAnswer?: boolean
  background?: boolean
  allowPaused?: boolean
  /** Make the child run a tool before its final response. */
  toolProof?: LiveChildToolProof
  /** Register provider-owned report handling while the real child remains held. */
  beforeRelease?: () => Promise<void>
  /** Complete provider-owned work that starts after the native child finishes. */
  afterComplete?: () => Promise<void>
}

/** The child that {@link exerciseLiveChildTranscript} followed. Its tab is the selected tab when the helper returns. */
export interface LiveChild {
  row: Locator
  childId: string
  parentId: string
}

/** The file that the child reads, and the computed marker that only that file holds. */
interface MarkerRead {
  filePath: string
  marker: string
}

/**
 * Script the child turns: an optional native tool call, then the native final response.
 * The gate holds only the final response.
 * Thus the result of the tool reaches the child tab while the child still runs.
 */
function liveChildRules(context: LiveChildContext, spec: LiveChildSpec, finalResponse: MockModelStep, read: MarkerRead | undefined): MockModelRule[] {
  if (read) {
    return [
      {
        name: 'the child reads its marker file',
        when: spec.childWhen,
        respond: { toolCalls: [readToolCall(context.provider, LIVE_CHILD_READ_CALL_ID, read.filePath)] },
        once: true,
      },
      // Only the Read result supplies the marker, so this rule answers the request that follows the Read.
      { name: 'the held child answer after the read', when: { body: read.marker }, respond: finalResponse, once: true },
    ]
  }
  if (spec.toolProof && 'shell' in spec.toolProof) {
    return [
      {
        name: 'the child runs its shell command',
        when: spec.childWhen,
        respond: { toolCalls: [bashToolCall(context.provider, LIVE_CHILD_SHELL_CALL_ID, spec.toolProof.shell.command)] },
        once: true,
      },
      // The same matcher as the rule above, which `once` has spent. This rule is not `once`, so a repeated child
      // request gets the held answer too.
      { name: 'the held child answer after the shell command', when: spec.childWhen, respond: finalResponse },
    ]
  }
  return [{ name: 'the held child answer', when: spec.childWhen, respond: finalResponse, once: true }]
}

/**
 * Hold a child model answer while its prompt and tool output are visible in a running child tab.
 * The Worker registry must be empty before the spawn, because the helper takes the first subagent row as the child.
 */
export async function exerciseLiveChildTranscript(context: LiveChildContext, spec: LiveChildSpec): Promise<LiveChild> {
  const { page, modelScript } = context
  // A chat bubble renders its text as Markdown, so each text check reads the words that the bubble shows.
  const taskWords = markdownToPlainText(spec.childTask)
  if (taskWords === '')
    throw new Error(`A live child needs a task that shows words in its tab, not ${JSON.stringify(spec.childTask)}.`)
  const gate = `live-child-${context.provider}`
  const parentGate = spec.holdParentAnswer ? `live-parent-${context.provider}` : undefined
  const finalResponse: MockModelStep = { ...(spec.childResponse ?? { text: DEFAULT_CHILD_ANSWER }), gate }
  // A final response that only calls a tool shows no answer words to check.
  const heldWords = finalResponse.text === undefined ? '' : markdownToPlainText(finalResponse.text)
  const read: MarkerRead | undefined = spec.toolProof && 'read' in spec.toolProof
    ? { filePath: join(spec.toolProof.read.workingDir, `live-child-${context.provider}.txt`), marker: uniqueMarker('CHILDREAD') }
    : undefined
  // The parent is the agent on screen before the child spawns.
  const parentId = await selectedAgentTabId(page)
  await expectNoRegistryRows(page, context.leapmuxServer)
  if (read)
    writeFileSync(read.filePath, `${read.marker}\n`)
  const child = await withCleanup(async (): Promise<LiveChild> => {
    await modelScript.rule(...liveChildRules(context, spec, finalResponse, read))
    await modelScript.queue(
      { toolCalls: [spawnSubagentToolCall(context.provider, LIVE_CHILD_SPAWN_CALL_ID, {
        description: 'Answer the live child task',
        prompt: modelScript.prompt(spec.childTask),
        ...(spec.background === undefined ? {} : { background: spec.background }),
      })] },
      { text: 'The child task ended.', ...(parentGate ? { gate: parentGate } : {}) },
    )
    await sendMessage(page, modelScript.prompt(spec.parentTask))
    await modelScript.waitForGate(gate)
    await spec.beforeRelease?.()
    const row = await requireRegistryRow(page)
    await expect(row).toHaveAttribute('data-status', spec.allowPaused ? /^(?:running|paused)$/ : 'running')
    const childId = await openChildTabFromRow(page, row)
    await expect(userBubbles(page).filter({ hasText: taskWords }).first()).toBeVisible()
    if (heldWords !== '')
      await expect(assistantBubbles(page).filter({ hasText: heldWords })).toHaveCount(0)
    if (read) {
      await expect(toolRows(page).filter({ hasText: basename(read.filePath) }).first()).toBeVisible()
      if (spec.toolProof && 'read' in spec.toolProof && spec.toolProof.read.expandResult)
        await expandNativeResultView(toolCallRow(page, LIVE_CHILD_READ_CALL_ID))
      await expect(messageContents(page).filter({ hasText: read.marker }).first()).toBeVisible()
      await tabById(page, parentId).click()
      await expect(messageContents(page).filter({ hasText: read.marker })).toHaveCount(0)
      await tabById(page, childId).click()
    }
    else if (spec.toolProof && 'shell' in spec.toolProof) {
      await expect(toolRows(page).filter({ hasText: spec.toolProof.shell.command }).first()).toBeVisible()
    }
    await modelScript.releaseGate(gate)
    if (parentGate) {
      await modelScript.waitForGate(parentGate)
      await modelScript.releaseGate(parentGate)
    }
    return { row, childId, parentId }
  }, () => finishCleanup([
    modelScript.releaseGateIfHeld(gate),
    ...(parentGate ? [modelScript.releaseGateIfHeld(parentGate)] : []),
  ]))
  await completeLiveChildTranscript(modelScript, async () => {
    if (heldWords !== '' && (spec.finalAnswerInChildTab ?? true))
      await expect(assistantBubbles(page).filter({ hasText: heldWords }).first()).toBeVisible()
    await spec.afterComplete?.()
  })
  return child
}

/** Finish the initial model steps before provider-owned completion work. */
export async function completeLiveChildTranscript(modelScript: Pick<ModelScript, 'waitForSteps'>, afterComplete?: () => Promise<void>): Promise<void> {
  await modelScript.waitForSteps()
  await afterComplete?.()
}

/** A file that a child reads, and the unique marker that only the file holds. */
export interface ChildMarkerFile {
  path: string
  marker: string
}

/** Write a file with a new unique marker into `directory`, for a child to read. */
export function writeChildMarkerFile(directory: string, fileName: string): ChildMarkerFile {
  if (directory.trim() === '')
    throw new Error('A child marker file needs the working directory of the agent.')
  if (fileName.trim() === '' || basename(fileName) !== fileName)
    throw new Error(`A child marker file needs a plain file name, not ${JSON.stringify(fileName)}.`)
  const file = { path: join(directory, fileName), marker: uniqueMarker('NATIVECHILDREAD') }
  writeFileSync(file.path, file.marker)
  return file
}

/** Require that no stored Worker message of the agent holds `text`. The read covers every page of the messages. */
export async function expectStoredMessagesLack(context: Pick<ManagedNativeScenarioContext, 'leapmuxServer'>, agentId: string, text: string): Promise<void> {
  if (text.trim() === '')
    throw new Error('A stored message check needs a text that is not empty, because every message holds an empty text.')
  const messages = await readAllAgentMessages(context, agentId)
  const holders = messages.filter(message => decompressContentToString(message.content, message.contentCompression)?.includes(text) ?? false)
  expect(holders.map(message => message.id), `no stored Worker message of agent ${agentId} holds ${JSON.stringify(text)}`).toEqual([])
}

/**
 * Prove that the result of a native Read of a held child stays out of its tab and out of the Worker store while the
 * child runs. Some providers deliver only the prompt and the report of a child, so its tool output never shows live:
 *
 * - The Read ran natively, and its result reached the model of the child.
 * - The child tab shows neither the marker nor a tool row of the file.
 * - No stored Worker message of the child holds the marker.
 * - The row of the child stays running.
 *
 * The helper then finishes the child. `restoredAfterCompletion` requires the marker in the child tab after the
 * completion and after a reload, for a provider that restores the transcript of a completed child.
 */
export async function expectChildToolOutputDeferred(
  context: Pick<ManagedNativeScenarioContext, 'page' | 'leapmuxServer'>,
  child: HeldNativeChild,
  options: { marker: string, fileName: string, readCallId: string, restoredAfterCompletion: boolean },
): Promise<void> {
  const { page } = context
  await withCleanup(async () => {
    expect(nativeToolResult(await child.heldRequest(), options.readCallId), 'the native Read result reached the model of the child').toContain(options.marker)
    await openChildTabFromRow(page, child.row)
    await expect(messageContents(page).filter({ hasText: options.marker })).toHaveCount(0)
    await expect(toolRows(page).filter({ hasText: options.fileName })).toHaveCount(0)
    await expectStoredMessagesLack(context, child.childId, options.marker)
    await expect(child.row).toHaveAttribute('data-status', 'running')
  }, child.finish)
  if (!options.restoredAfterCompletion)
    return
  await tabById(page, child.childId).click()
  await expect(messageContents(page).filter({ hasText: options.marker }).first()).toBeVisible()
  await page.reload()
  await expect(messageContents(page).filter({ hasText: options.marker }).first()).toBeVisible()
}
