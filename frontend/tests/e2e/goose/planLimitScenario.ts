import type { ManagedNativeScenarioContext } from '../helpers/nativeScenario'
import { existsSync } from 'node:fs'
import { join } from 'node:path'
import { expect } from '@playwright/test'
import { sendNativeAnswer } from '../helpers/nativeConversation'
import { currentNativeAgent, nativeModelToolNames } from '../helpers/nativeScenario'
import { nativeToolResult } from '../helpers/nativeToolResult'
import { bashToolCall } from '../helpers/providerToolCalls'
import { quotePosixShellArgument } from '../helpers/shellArguments'
import { chooseSettingsOption, expectSettingsChip, sendMessage, waitForAgentIdle, waitForSettingsIdle } from '../helpers/ui'

/** Exercise Goose's actual Chat mode and its native refusal to run a tool. */
export async function exerciseGoosePlanLimit(context: ManagedNativeScenarioContext): Promise<void> {
  const before = await currentNativeAgent(context)
  if (!before.workingDir)
    throw new Error('The native Chat mode proof requires a private working directory.')
  const modes = before.optionGroups.find(group => group.id === 'permissionMode')?.options.map(option => option.id)
  expect(modes).toEqual(expect.arrayContaining(['chat', 'auto', 'approve', 'smart_approve']))
  expect(modes).not.toContain('plan')
  const baseline = await sendNativeAnswer(context, 'Report the native tool catalog before Chat mode.', 'The native execution mode is ready.')
  const names = nativeModelToolNames(baseline)
  expect(names.length).toBeGreaterThan(0)
  expect(names.some(name => /(?:enter|exit)[_-]?plan/i.test(name))).toBe(false)
  await chooseSettingsOption(context.page, 'permissionMode-chat')
  await waitForSettingsIdle(context.page)
  await expectSettingsChip(context.page, 'Chat')
  const file = join(before.workingDir, 'native-chat-plan-must-not-run.txt')
  const start = (await context.modelScript.status()).stepCount
  const callId = 'goose-native-chat-tool'
  await context.modelScript.queue(
    { toolCalls: [bashToolCall(context.provider, callId, `printf native-chat-tool > ${quotePosixShellArgument(file)}`)] },
    { text: 'The native Chat mode skips tools and has no plan approval.' },
  )
  await sendMessage(context.page, context.modelScript.prompt('Try the supplied tool while the native mode is Chat.'))
  await context.modelScript.waitForSteps(start + 2)
  await waitForAgentIdle(context.page)
  const request = (await context.modelScript.status()).requests.find(record => record.stepIndex === start + 1)
  expect(nativeToolResult(request, callId)).toContain('goose chat mode')
  expect(existsSync(file)).toBe(false)
  expect((await currentNativeAgent(context)).optionGroups.find(group => group.id === 'permissionMode')?.currentValue).toBe('chat')
}
