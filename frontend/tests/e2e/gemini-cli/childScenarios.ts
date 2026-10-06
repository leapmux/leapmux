import type { ManagedNativeScenarioContext } from '../helpers/nativeScenario'
import type { RunningNativeChild } from '../helpers/unsupportedSubagent'
import { readFileSync, writeFileSync } from 'node:fs'
import { join } from 'node:path'
import { expect } from '@playwright/test'
import { isObject } from '../../../src/lib/jsonPick'
import { nativeMessageBody, readNativeMessageSnapshot } from '../helpers/nativeMessages'
import { currentNativeAgent, nativeAgentById } from '../helpers/nativeScenario'
import { nativeToolResult } from '../helpers/nativeToolResult'
import { geminiCompleteTaskToolCall, readToolCall, spawnSubagentToolCall } from '../helpers/providerToolCalls'
import { openRunningNativeChild } from '../helpers/runningChildProof'
import { uniqueMarker } from '../helpers/shellArguments'
import { openChildTabFromRow } from '../helpers/subagentRegistry'
import { chooseSettingsOption, messageContents, waitForSettingsIdle } from '../helpers/ui'
import { geminiNativeProject } from './nativeStore'

// The installed child system prompt states this rule. The parent does not.
const CHILD_SYSTEM = 'When you have completed your task, you MUST call the `complete_task` tool'

export interface GeminiRunningChild extends RunningNativeChild {
  prompt: string
  progress: string
  thought: string
  fileMarker: string
  finalReply: string
  nativeChildId: string
}

/** Hold a real native child after its Read while its complete source records exist. */
export async function openGeminiRunningChild(context: ManagedNativeScenarioContext, index = 0): Promise<GeminiRunningChild> {
  await chooseSettingsOption(context.page, 'permissionMode-yolo')
  await waitForSettingsIdle(context.page)
  const parent = await currentNativeAgent(context)
  const marker = uniqueMarker()
  const prompt = context.modelScript.prompt(`GEMINICHILDPROMPT${marker}: read the actual child file and complete the native task.`)
  const progress = `GEMINICHILDPROGRESS${marker}`
  const thought = `GEMINICHILDTHOUGHT${marker}`
  const fileMarker = `GEMINICHILDFILE${marker}`
  const finalReply = `GEMINICHILDFINAL${marker}`
  const path = join(parent.workingDir, `gemini-child-${index}.txt`)
  writeFileSync(path, `${fileMarker}\n`)
  const gate = `gemini-child-${index}-${marker}`
  const child = await openRunningNativeChild(context, {
    spawn: spawnSubagentToolCall(context.provider, `gemini-child-spawn-${index}-${marker}`, { description: 'Read the native child file', prompt }),
    gate,
    child: {
      matcher: { system: CHILD_SYSTEM, user: prompt },
      finalStep: { toolCalls: [geminiCompleteTaskToolCall(`gemini-child-complete-${marker}`, finalReply)] },
    },
    rules: [{ name: 'the native child reads its actual file', when: { system: CHILD_SYSTEM, user: prompt }, respond: { reasoning: thought, text: progress, toolCalls: [readToolCall(context.provider, `gemini-child-read-${marker}`, path)] }, once: true }],
    allowExistingRows: index > 0,
  })
  expect(nativeToolResult(await child.heldRequest(), `gemini-child-read-${marker}`)).toContain(fileMarker)
  const info = await nativeAgentById(context, child.childId)
  if (!info || !info.providerChildKey)
    throw new Error('The native child has no durable provider UUID.')
  expect(info.parentAgentId).toBe(parent.id)
  expect(info.rootAgentId).toBe(parent.rootAgentId)
  expect(info.spawnSpanId).toBe('')
  expect(info.providerChildKey).toMatch(/^[a-f0-9]{8}(?:-[a-f0-9]{4}){3}-[a-f0-9]{12}$/)
  const nativePath = join(geminiNativeProject(context, parent), 'chats', parent.agentSessionId, `${info.providerChildKey}.jsonl`)
  const firstLine = readFileSync(nativePath, 'utf8').split('\n')[0]
  const metadata: unknown = JSON.parse(firstLine ?? '')
  expect(metadata).toMatchObject({ sessionId: info.providerChildKey, kind: 'subagent' })
  const snapshot = await readNativeMessageSnapshot(context, child.childId)
  expect(snapshot.messages.length).toBeGreaterThan(0)
  expect(snapshot.messages.map(message => nativeMessageBody(message)).filter(isObject).every(frame => !('sessionUpdate' in frame))).toBe(true)
  return { ...child, prompt, progress, thought, fileMarker, finalReply, nativeChildId: info.providerChildKey }
}

/** Inspect the child tab while its model call remains held. */
export async function expectGeminiLiveChild(context: ManagedNativeScenarioContext, child: GeminiRunningChild): Promise<void> {
  await openChildTabFromRow(context.page, child.row)
  for (const text of [child.prompt, child.progress, child.thought, child.fileMarker])
    await expect(messageContents(context.page).filter({ hasText: text }).first()).toBeVisible()
  await expect(messageContents(context.page).filter({ hasText: child.finalReply })).toHaveCount(0)
  await expect(child.row).toHaveAttribute('data-status', 'running')
}

/** Complete the native task and verify its stored child transcript after reload. */
export async function finishGeminiChildWithReload(
  context: ManagedNativeScenarioContext,
  child: GeminiRunningChild,
  options: { beforeReload?: () => void | Promise<void> } = {},
): Promise<void> {
  await child.finish()
  await expect(child.row).toHaveAttribute('data-status', 'completed')
  await openChildTabFromRow(context.page, child.row)
  await expect(messageContents(context.page).filter({ hasText: child.finalReply }).first()).toBeVisible()
  await options.beforeReload?.()
  await context.page.reload()
  for (const text of [child.prompt, child.progress, child.thought, child.fileMarker, child.finalReply])
    await expect(messageContents(context.page).filter({ hasText: text }).first()).toBeVisible()
}
