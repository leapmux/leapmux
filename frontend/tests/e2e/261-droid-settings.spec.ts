import { readFileSync, writeFileSync } from 'node:fs'
import { join } from 'node:path'
import { AgentProvider } from '../../src/generated/proto/leapmux/v1/agent_pb'
import { DROID_E2E_SKIP_REASON, DROID_TITLE_RULE, droidTest, expect } from './droid-fixtures'
import { droidNativeSettingsUpdates } from './helpers/droidNativeSettings'
import { DROID_MOCK_MODEL_IDS, MOCK_MODELS } from './helpers/mockAgentEnvironment'
import { editToolCall, readToolCall } from './helpers/providerToolCalls'
import {
  applyPermissionPreset,
  assistantBubbles,
  chooseSettingsOption,
  closeComposerMenus,
  expectSettingsOptionChosen,
  openPlusMenu,
  sendMessage,
  settingsGroupTrigger,
  waitForAgentIdle,
  waitForSettingsHydrated,
  waitForSettingsIdle,
} from './helpers/ui'

/**
 * 261 — Factory Droid settings.
 *
 * The isolated BYOK settings pin two mock models. The built-in model also
 * reaches the mock through Droid's isolated API base URL. Native settings
 * events and later model requests show whether each choice takes effect.
 */
droidTest.skip(!!DROID_E2E_SKIP_REASON, DROID_E2E_SKIP_REASON || '')

droidTest.describe('Factory Droid settings', () => {
  droidTest('offers the model, effort and permission-mode groups', async ({ authenticatedDroidWorkspace, page }) => {
    void authenticatedDroidWorkspace
    await waitForSettingsHydrated(page)
    await openPlusMenu(page)
    await expect(settingsGroupTrigger(page, 'model')).toBeVisible()
    await expect(settingsGroupTrigger(page, 'effort')).toBeVisible()
    await expect(settingsGroupTrigger(page, 'permissionMode')).toBeVisible()
    await closeComposerMenus(page)
  })

  droidTest('sends a selected custom model to the mock and keeps it after reload', async ({ authenticatedDroidWorkspace, page, modelScript, leapmuxServer }) => {
    await waitForSettingsHydrated(page)
    await chooseSettingsOption(page, `model-${DROID_MOCK_MODEL_IDS.alternate}`)
    await waitForSettingsIdle(page)
    await expect.poll(async () => (await droidNativeSettingsUpdates(leapmuxServer, authenticatedDroidWorkspace.workspaceId)).some(update =>
      update.requestId?.startsWith('leapmux-') && update.modelId === DROID_MOCK_MODEL_IDS.alternate)).toBe(true)
    await expectSettingsOptionChosen(page, `model-${DROID_MOCK_MODEL_IDS.alternate}`)

    await modelScript.rule(DROID_TITLE_RULE)
    await modelScript.queue({ text: 'The alternate model answered.' })
    await sendMessage(page, modelScript.prompt('Reply through the selected model.'))
    await modelScript.waitForSteps()
    await waitForAgentIdle(page)
    const request = (await modelScript.status()).requests.find(record => record.stepIndex === 0)
    expect(request?.body).toMatchObject({ model: MOCK_MODELS.droidAlt })
    await expect(assistantBubbles(page).filter({ hasText: 'The alternate model answered.' }).first()).toBeVisible()
    await page.reload()
    await waitForSettingsHydrated(page)
    await expectSettingsOptionChosen(page, `model-${DROID_MOCK_MODEL_IDS.alternate}`)
  })

  droidTest('sends a built-in model effort to the isolated mock', async ({ authenticatedDroidWorkspace, page, modelScript, leapmuxServer }) => {
    await waitForSettingsHydrated(page)
    await chooseSettingsOption(page, 'model-claude-fable-5.1')
    await waitForSettingsIdle(page)
    await chooseSettingsOption(page, 'effort-high')
    await waitForSettingsIdle(page)

    await expect.poll(async () => (await droidNativeSettingsUpdates(leapmuxServer, authenticatedDroidWorkspace.workspaceId)).some(update =>
      update.requestId?.startsWith('leapmux-') && update.modelId === 'claude-fable-5.1' && update.reasoningEffort === 'high')).toBe(true)
    await expectSettingsOptionChosen(page, 'effort-high')

    await modelScript.rule(DROID_TITLE_RULE)
    await modelScript.queue({ text: 'Droid answered at high effort.' })
    await sendMessage(page, modelScript.prompt('Reply once after the effort switch.'))
    const status = await modelScript.waitForSteps()
    await waitForAgentIdle(page)
    const request = status.requests.find(record => record.stepIndex === 0)
    expect(request?.path).toBe('/v1/api/llm/a/v1/messages')
    expect(request?.body).toMatchObject({ model: 'claude-fable-5.1', output_config: { effort: 'high' } })

    await chooseSettingsOption(page, `model-${DROID_MOCK_MODEL_IDS.primary}`)
    await waitForSettingsIdle(page)
  })

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

    await modelScript.rule(DROID_TITLE_RULE)
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
