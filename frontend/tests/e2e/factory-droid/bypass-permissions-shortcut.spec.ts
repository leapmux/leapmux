import { readFileSync, writeFileSync } from 'node:fs'
import { join } from 'node:path'
import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { droidTest, expect } from '../droid-fixtures'
import { droidNativeSettingsUpdates } from '../helpers/droidNativeSettings'
import { editToolCall, readToolCall } from '../helpers/providerToolCalls'
import { applyPermissionPreset, expectSettingsOptionChosen, sendMessage, waitForAgentIdle, waitForSettingsHydrated, waitForSettingsIdle } from '../helpers/ui'

droidTest.describe('Factory Droid settings', () => {
  droidTest('applies bypass to the native session before an edit', async ({ askingDroidWorkspace, page, modelScript, leapmuxServer }) => {
    const filename = 'droid-bypass-note.txt'
    const path = join(askingDroidWorkspace.workingDir, filename)
    writeFileSync(path, 'before')
    await waitForSettingsHydrated(page)
    await applyPermissionPreset(page, 'bypass')
    await waitForSettingsIdle(page)
    await expect.poll(async () => (await droidNativeSettingsUpdates(leapmuxServer, askingDroidWorkspace.workspaceId)).some(update =>
      update.requestId?.startsWith('leapmux-') && update.interactionMode === 'auto' && update.autonomyLevel === 'high')).toBe(true)
    await expectSettingsOptionChosen(page, 'permissionMode-auto-high')

    await modelScript.queue(
      { toolCalls: [readToolCall(AgentProvider.DROID, 'bypass-read', path)] },
      { toolCalls: [editToolCall(AgentProvider.DROID, 'bypass-edit', { path, before: 'before', after: 'after' })] },
      { text: 'The edit completed.' },
    )
    await sendMessage(page, modelScript.prompt('Replace before with after in the note.'))
    const status = await modelScript.waitForSteps()
    expect(status.requests.find(request => request.stepIndex === 1)?.body).toMatchObject({
      messages: expect.arrayContaining([expect.objectContaining({ role: 'tool', content: expect.stringContaining('before') })]),
    })
    await waitForAgentIdle(page)
    await expect(page.locator('[data-testid="control-banner"]:visible')).toHaveCount(0)
    expect(readFileSync(path, 'utf8')).toBe('after')
  })
})
