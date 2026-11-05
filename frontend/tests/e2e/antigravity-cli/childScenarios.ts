import type { ManagedNativeScenarioContext } from '../helpers/nativeScenario'
import type { RunningNativeChild } from '../helpers/runningChildProof'
import { writeFileSync } from 'node:fs'
import { join } from 'node:path'
import { expect } from '@playwright/test'
import { isObject } from '../../../src/lib/jsonPick'
import { nativeMessageBody, readNativeMessageSnapshot } from '../helpers/nativeMessages'
import { currentNativeAgent, nativeAgentById } from '../helpers/nativeScenario'
import { spawnSubagentToolCall, viewFileToolCall } from '../helpers/providerToolCalls'
import { openRunningNativeChild } from '../helpers/runningChildProof'
import { uniqueMarker } from '../helpers/shellArguments'
import { openChildTabFromRow } from '../helpers/subagentRegistry'
import { chooseSettingsOption, messageContents, waitForSettingsIdle } from '../helpers/ui'
import { antigravityNativeToolResult } from './toolResult'

export interface AntigravityRunningChild extends RunningNativeChild {
  prompt: string
  progress: string
  thought: string
  fileMarker: string
  finalReply: string
  nativeChildId: string
}

/** Hold a real native child after its Read while its complete source records exist. */
export async function openAntigravityRunningChild(context: ManagedNativeScenarioContext, index = 0): Promise<AntigravityRunningChild> {
  await chooseSettingsOption(context.page, 'permissionMode-bypass')
  await waitForSettingsIdle(context.page)
  const parent = await currentNativeAgent(context)
  const marker = uniqueMarker()
  const task = `GEMINICHILDPROMPT${marker}: read the actual child file and complete the native task.`
  const scriptedPrompt = context.modelScript.prompt(task)
  const progress = `GEMINICHILDPROGRESS${marker}`
  const thought = `GEMINICHILDTHOUGHT${marker}`
  const fileMarker = `GEMINICHILDFILE${marker}`
  const finalReply = `GEMINICHILDFINAL${marker}`
  const path = join(parent.workingDir, `antigravity-child-${index}.txt`)
  writeFileSync(path, `${fileMarker}\n`)
  const gate = `antigravity-child-${index}-${marker}`
  const childTurn = { user: scriptedPrompt }
  await context.modelScript.fallback({ text: 'The native parent finished its wake turn.' })
  const child = await openRunningNativeChild(context, {
    spawn: spawnSubagentToolCall(context.provider, `antigravity-child-spawn-${index}-${marker}`, { description: 'Read the native child file', prompt: scriptedPrompt }),
    gate,
    child: {
      matcher: childTurn,
      finalStep: { text: finalReply },
    },
    rules: [
      {
        name: 'the native child reads its actual file',
        when: childTurn,
        respond: {
          reasoning: thought,
          text: progress,
          toolCalls: [
            viewFileToolCall(`antigravity-child-read-${marker}`, path),
          ],
        },
        once: true,
      },
    ],
    allowExistingRows: index > 0,
  })
  expect(antigravityNativeToolResult(await child.heldRequest(), `antigravity-child-read-${marker}`)).toContain(fileMarker)
  const info = await nativeAgentById(context, child.childId)
  if (!info || !info.providerChildKey)
    throw new Error('The native child has no durable provider UUID.')
  expect(info.parentAgentId).toBe(parent.id)
  expect(info.rootAgentId).toBe(parent.rootAgentId)
  expect(info.spawnSpanId).toBe(`antigravity-child-spawn-${index}-${marker}`)
  expect(info.providerChildKey).toMatch(/^[a-f0-9]{8}(?:-[a-f0-9]{4}){3}-[a-f0-9]{12}$/)
  const snapshot = await readNativeMessageSnapshot(context, child.childId)
  expect(snapshot.messages.length).toBeGreaterThan(0)
  const frames = snapshot.messages.map(message => nativeMessageBody(message)).filter(isObject)
  expect(frames.every(frame => !('sessionUpdate' in frame))).toBe(true)
  expect(frames.some(frame => frame.type === 'CORTEX_STEP_TYPE_USER_INPUT' && JSON.stringify(frame).includes(task)), JSON.stringify(frames, null, 2)).toBe(true)
  return {
    ...child,
    prompt: task,
    progress,
    thought,
    fileMarker,
    finalReply,
    nativeChildId: info.providerChildKey,
  }
}

/** Inspect the child tab while its model call remains held. */
export async function expectAntigravityLiveChild(context: ManagedNativeScenarioContext, child: AntigravityRunningChild): Promise<void> {
  await openChildTabFromRow(context.page, child.row)
  for (const text of [
    child.prompt,
    child.progress,
    child.thought,
    child.fileMarker,
  ])
    await expect(messageContents(context.page).filter({ hasText: text }).first()).toBeVisible()
  await expect(messageContents(context.page).filter({ hasText: child.finalReply })).toHaveCount(0)
  await expect(child.row).toHaveAttribute('data-status', 'running')
}

/** Complete the native task and verify its stored child transcript after reload. */
export async function finishAntigravityChildWithReload(
  context: ManagedNativeScenarioContext,
  child: AntigravityRunningChild,
  options: { beforeReload?: () => void | Promise<void> } = {},
): Promise<void> {
  await child.finish()
  await expect(child.row).toHaveAttribute('data-status', 'succeeded')
  await openChildTabFromRow(context.page, child.row)
  await expect(messageContents(context.page).filter({ hasText: child.finalReply }).first()).toBeVisible()
  await options.beforeReload?.()
  await context.page.reload()
  for (const text of [
    child.prompt,
    child.progress,
    child.thought,
    child.fileMarker,
    child.finalReply,
  ])
    await expect(messageContents(context.page).filter({ hasText: text }).first()).toBeVisible()
}
