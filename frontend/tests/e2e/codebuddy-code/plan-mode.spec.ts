import { expect } from '@playwright/test'
import { codebuddyTest } from '../codebuddy-fixtures'
import { sendNativeAnswer } from '../helpers/nativeConversation'
import { currentNativeAgent, nativeModelInstructionText } from '../helpers/nativeScenario'
import { chooseSettingsOption, expectSettingsOptionChosen, waitForSettingsHydrated, waitForSettingsIdle } from '../helpers/ui'
import { codebuddyPlanOptionSnapshot } from './planMode'
import { nativeContext } from './scenarios'

codebuddyTest('adds actual native planning instructions and preserves the selected mode after reload', async ({ codebuddyWorkspace, page, modelScript, leapmuxServer }) => {
  const context = await nativeContext({ page, modelScript, leapmuxServer, workspaceId: codebuddyWorkspace.workspaceId })
  await waitForSettingsHydrated(page)
  await chooseSettingsOption(page, 'permissionMode-default')
  await waitForSettingsIdle(page)
  const before = await currentNativeAgent(context)
  const prompt = 'MODEPROBE return one short response.'
  const normal = await sendNativeAnswer(context, prompt, 'DEFAULT_MODE_REPLY')
  const normalLines = new Set(nativeModelInstructionText(normal).split(/\r?\n/).map(line => line.trim()))
  await chooseSettingsOption(page, 'permissionMode-plan')
  await waitForSettingsIdle(page)
  await expectSettingsOptionChosen(page, 'permissionMode-plan')
  await page.reload()
  await waitForSettingsHydrated(page)
  await expectSettingsOptionChosen(page, 'permissionMode-plan')
  const planned = await sendNativeAnswer(context, prompt, 'PLAN_MODE_REPLY')
  const additions = nativeModelInstructionText(planned).split(/\r?\n/).map(line => line.trim()).filter(line => line && !normalLines.has(line)).join('\n')
  expect(additions).toMatch(/\bplan(?:ning)?\b|read-only|\bread only\b/i)
  const after = await currentNativeAgent(context)
  expect(codebuddyPlanOptionSnapshot(after.optionGroups)).toEqual(codebuddyPlanOptionSnapshot(before.optionGroups))
})
