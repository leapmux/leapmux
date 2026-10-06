import { readFileSync, writeFileSync } from 'node:fs'
import { join } from 'node:path'
import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { droidTest, expect } from '../droid-fixtures'
import { editToolCall, readToolCall } from '../helpers/providerToolCalls'
import { applyPermissionPreset, expectNoControlBanner, expectSettingsOptionChosen, sendMessage, waitForAgentIdle, waitForSettingsHydrated, waitForSettingsIdle } from '../helpers/ui'
import { expectDroidNativeSettings } from './settingsUpdates'

droidTest.describe('Factory Droid settings', () => {
  droidTest('applies bypass to the native session before an edit', async ({ askingDroidWorkspace, page, modelScript, leapmuxServer }) => {
    const filename = 'droid-bypass-note.txt'
    const path = join(askingDroidWorkspace.workingDir, filename)
    writeFileSync(path, 'before')
    await waitForSettingsHydrated(page)
    await applyPermissionPreset(page, 'bypass')
    await waitForSettingsIdle(page)
    await expectDroidNativeSettings({ page, leapmuxServer }, { interactionMode: 'auto', autonomyLevel: 'high' })
    await expectSettingsOptionChosen(page, 'permissionMode-auto-high')

    const start = await modelScript.queue(
      { toolCalls: [readToolCall(AgentProvider.DROID, 'bypass-read', path)] },
      { toolCalls: [editToolCall(AgentProvider.DROID, 'bypass-edit', { path, before: 'before', after: 'after' })] },
      { text: 'The edit completed.' },
    )
    await sendMessage(page, modelScript.prompt('Replace before with after in the note.'))
    await modelScript.waitForSteps(start + 3)
    await waitForAgentIdle(page)
    // The request after the read holds the read result, which states the bytes before the edit.
    expect((await modelScript.requestAt(start + 1)).body).toMatchObject({
      messages: expect.arrayContaining([expect.objectContaining({ role: 'tool', content: expect.stringContaining('before') })]),
    })
    await expectNoControlBanner(page)
    expect(readFileSync(path, 'utf8')).toBe('after')
  })
})
