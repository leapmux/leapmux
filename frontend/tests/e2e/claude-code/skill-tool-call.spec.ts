import { expect } from '@playwright/test'
import { claudeTest } from '../claude-fixtures'
import { claudeSkillToolCall } from '../helpers/providerToolCalls'
import { assistantBubbles, chooseSettingsOption, sendMessage, userBubbles, waitForAgentIdle, waitForSettingsIdle } from '../helpers/ui'

claudeTest('a skill that the model loads draws no user bubble', async ({ native }) => {
  const { page, modelScript } = native
  await chooseSettingsOption(page, 'permissionMode-bypassPermissions')
  await waitForSettingsIdle(page)
  const start = await modelScript.queue(
    { toolCalls: [claudeSkillToolCall('skill-1', 'keybindings-help')] },
    { text: 'SKILL_LOADED' },
  )
  await sendMessage(page, modelScript.prompt('Load the keybindings-help skill and acknowledge it.'))
  await modelScript.waitForSteps(start + 2)
  await waitForAgentIdle(page)
  await expect(assistantBubbles(page).filter({ hasText: 'SKILL_LOADED' })).toHaveCount(1)
  await expect(userBubbles(page).filter({ hasText: '# Keybindings Skill' })).toHaveCount(0)
})
