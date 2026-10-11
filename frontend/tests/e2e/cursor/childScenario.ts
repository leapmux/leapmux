import type { ManagedNativeScenarioContext } from '../helpers/nativeScenario'
import type { RunningNativeChild } from '../helpers/runningChildProof'
import { writeFileSync } from 'node:fs'
import { join } from 'node:path'
import { expect } from '@playwright/test'
import { withCleanup } from '../helpers/cleanup'
import { currentNativeAgent, toolTurnSteps } from '../helpers/nativeScenario'
import { readToolCall, spawnSubagentToolCall } from '../helpers/providerToolCalls'
import { openRunningNativeChild } from '../helpers/runningChildProof'
import { uniqueMarker } from '../helpers/shellArguments'
import { expectNoRegistryRows, expectRowBecomesFinal, expectSectionPersists, openChildTabFromRow, requireRegistryRow } from '../helpers/subagentRegistry'
import { assistantBubbles, sendMessage, subagentReportBubble, userBubbles, waitForAgentIdle } from '../helpers/ui'

export interface CursorRunningChild extends RunningNativeChild {
  prompt: string
  marker: string
  answer: string
  filePath: string
}

/** Hold an actual local child after its real Read result reaches its own transcript. */
export async function openCursorRunningChild(
  context: ManagedNativeScenarioContext,
  options: { allowExistingRows?: boolean } = {},
): Promise<CursorRunningChild> {
  const parent = await currentNativeAgent(context)
  if (!parent.workingDir)
    throw new Error('The actual Cursor child requires a private working directory.')
  const suffix = uniqueMarker()
  const gate = `cursor-native-child-${suffix}`
  const prompt = `NATIVECURSORCHILD${suffix}: read the supplied file and report once.`
  const marker = `CURSORCHILDREAD${suffix}`
  const answer = `CURSORCHILDFINAL${suffix}`
  const filePath = join(parent.workingDir, `native-child-${suffix}.txt`)
  writeFileSync(filePath, `${marker}\n`)
  const description = `Read the actual child file ${suffix}`
  const spawn = spawnSubagentToolCall(context.provider, `cursor-native-task-${suffix}`, {
    description,
    prompt: context.modelScript.prompt(prompt),
    nativeExecution: { modelId: 'mock-grok' },
  })
  const child = await openRunningNativeChild(context, {
    spawn,
    gate,
    // The Cursor parent states its Task call and its answer in one model turn.
    parentSteps: toolTurnSteps([spawn], { text: 'The native parent received its child report.' }, 'same-step'),
    ...(options.allowExistingRows === undefined ? {} : { allowExistingRows: options.allowExistingRows }),
    rowText: description,
    rules: [{
      name: 'the actual Cursor child reads its private file',
      when: { user: `^NATIVECURSORCHILD${suffix}:` },
      respond: {
        toolCalls: [readToolCall(context.provider, `cursor-native-read-${suffix}`, filePath)],
        text: answer,
        stream: { chunkChars: 1, delayMs: 0, gates: [{ afterChunk: 1, name: gate }] },
      },
      once: true,
    }],
  })
  return { ...child, prompt, marker, answer, filePath }
}

/**
 * Delegate one word to the remote Task service of Cursor, and require a registry row without a control character,
 * and the prompt and report of the child in its own tab. The transcript tab cell and the background task cell both
 * run this scenario.
 */
export async function exerciseCursorTaskDelegation(context: ManagedNativeScenarioContext): Promise<void> {
  const { page, modelScript } = context
  await expectNoRegistryRows(page, context.leapmuxServer)

  // The remote Task service supplies this report without starting a local child.
  const start = await modelScript.queue({
    toolCalls: [spawnSubagentToolCall(context.provider, 'spawn-cursor', {
      description: 'Ask the subagent for one word',
      prompt: 'Reply with the single word PONG.',
      report: 'PONG',
    })],
  })
  await sendMessage(page, modelScript.prompt('Delegate one word to a subagent.'))
  await modelScript.waitForSteps(start + 1)
  await waitForAgentIdle(page)

  const row = await requireRegistryRow(page)

  // Regression guard: the row's testid/data attributes must never contain a
  // control character (the embedded-newline toolCallId quirk is sanitized in
  // the neutral layer before it reaches the DOM). Built without a control-char
  // regex literal so no-control-regex stays satisfied.
  const rowHtml = await row.evaluate(el => el.outerHTML)
  const hasControlChar = Array.from(rowHtml).some(ch => (ch.codePointAt(0) ?? 0) < 0x20)
  expect(hasControlChar).toBe(false)

  await expectRowBecomesFinal(page, row)
  await expectSectionPersists(page)
  await openChildTabFromRow(page, row)
  // The child's PROMPT opens its transcript, and its REPORT closes it.
  //
  // Cursor omits the report from ACP and reads it back from its own session
  // store, and the mock writes that store over the KV channel -- see
  // `cursorSetBlob`. This assertion proves that the transcript reaches disk
  // and not only the screen.
  await expect(userBubbles(page).filter({ hasText: 'Reply with the single word PONG' })).toBeVisible()
  await expect(subagentReportBubble(page, /PONG/)).toBeVisible()
}

/**
 * Hold an actual local child, then require its identity and prompt in its own tab while it runs, and its final reply
 * after it completes and after a reload. The transcript tab cell and the background task cell both run this scenario.
 */
export async function exerciseCursorChildIdentity(context: ManagedNativeScenarioContext): Promise<void> {
  const { page } = context
  const child = await openCursorRunningChild(context)
  await withCleanup(async () => {
    await expect(child.row).toHaveAttribute('data-child-agent-id', child.childId)
    await expect(child.row).toHaveAttribute('data-status', 'running')
    expect(await openChildTabFromRow(page, child.row)).toBe(child.childId)
    await expect(userBubbles(page).filter({ hasText: child.prompt }).first()).toBeVisible()
  }, child.finish)
  // `finish` requires a final row status.
  await expectSectionPersists(page)
  expect(await openChildTabFromRow(page, child.row)).toBe(child.childId)
  await expect(assistantBubbles(page).filter({ hasText: child.answer }).first()).toBeVisible()
  await page.reload()
  await expect(assistantBubbles(page).filter({ hasText: child.answer }).first()).toBeVisible()
}
