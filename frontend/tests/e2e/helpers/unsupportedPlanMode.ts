import type { ManagedNativeScenarioContext } from './nativeScenario'
import { randomUUID } from 'node:crypto'
import { expect } from '@playwright/test'
import { sendNativeAnswer } from './nativeConversation'
import { currentNativeAgent, nativeModelContextText, nativeTextStep } from './nativeScenario'
import { waitForNativeToolSteps } from './nativeToolExecution'
import { nativeToolResult } from './nativeToolResult'
import { bashToolCall } from './providerToolCalls'
import { messageContents, openPlusMenu, sendMessage, waitForNativeSettingsHydrated } from './ui'

/** Prove that the launched protocol carries /plan as text and offers no Plan setting. */
export async function exerciseMissingNativePlanMode(context: ManagedNativeScenarioContext, options: { reload?: boolean } = {}): Promise<void> {
  const marker = randomUUID().replaceAll('-', '')
  await sendNativeAnswer(context, `Keep NOPLANCONTEXT${marker} for the next command.`, 'The actual native mode is ready.')
  const callId = `no-plan-${marker}`
  const output = `NOPLAN${marker}42`
  const start = await context.modelScript.queue(
    { toolCalls: [bashToolCall(context.provider, callId, `printf 'NOPLAN${marker}%s\\n' "$((40 + 2))"`)] },
    nativeTextStep(context, 'The literal plan command reached a working native tool turn.'),
  )
  await sendMessage(context.page, '/plan')
  await waitForNativeToolSteps(context, start + 2)
  const request = await context.modelScript.requestAt(start)
  expect(nativeModelContextText(request)).toContain('/plan')
  expect(nativeModelContextText(request)).toContain(`NOPLANCONTEXT${marker}`)
  const resultRequest = await context.modelScript.requestAt(start + 1)
  const result = context.readToolResult ? await context.readToolResult(resultRequest, callId) : { text: nativeToolResult(resultRequest, callId) }
  expect(result.text).toContain(output)
  await expect(messageContents(context.page).filter({ hasText: output }).first()).toBeVisible()
  for (const reload of options.reload === false ? [false] : [false, true]) {
    if (reload) {
      await context.page.reload()
      await waitForNativeSettingsHydrated(context.page)
    }
    const agent = await currentNativeAgent(context)
    expect(agent.optionGroups.length).toBeGreaterThan(0)
    const choices = agent.optionGroups.flatMap(group => group.options)
    expect(choices.length).toBeGreaterThan(0)
    expect(choices.some(option => /^plan$/i.test(option.id) || /^plan$/i.test(option.name))).toBe(false)
    const menu = await openPlusMenu(context.page)
    await expect(menu.locator('[data-testid$="-plan"]:visible')).toHaveCount(0)
    await context.page.keyboard.press('Escape')
  }
}
