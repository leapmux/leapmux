import type { MockModelRequestRecord } from '../helpers/mockModelScript'
import type { ManagedNativeScenarioContext } from '../helpers/nativeScenario'
import { expect } from '@playwright/test'
import { isObject } from '../../../src/lib/jsonPick'
import { claudeTest, claudeProcessTest as test } from '../claude-fixtures'
import { sendNativeAnswer } from '../helpers/nativeConversation'
import { nativeMessageBody, readNativeMessageSnapshot } from '../helpers/nativeMessages'
import { currentNativeAgent } from '../helpers/nativeScenario'
import { exerciseNativeOption } from '../helpers/nativeSettings'
import { chooseSettingsOption, expectAssistantAnswer, expectNoSettingsChip, expectSettingsChip, openSettingsMenu, sendMessage, settingsBar, visibleOnly, waitForAgentIdle, waitForSettingsIdle } from '../helpers/ui'

/** The CLI result reports the model's context window, including models with a native 1M window. */
async function expectNativeOpus1M(context: ManagedNativeScenarioContext, request: MockModelRequestRecord): Promise<void> {
  expect(request.protocol).toBe('anthropic-messages')
  expect(request.body).toMatchObject({
    model: expect.stringMatching(/^claude-opus-/),
  })
  await waitForAgentIdle(context.page)
  const agent = await currentNativeAgent(context)
  const snapshot = await readNativeMessageSnapshot(context, agent.id)
  const result = snapshot.messages.map(nativeMessageBody).filter(isObject).findLast(frame => frame.type === 'result')
  if (!result || !isObject(result.modelUsage) || !isObject(request.body) || typeof request.body.model !== 'string')
    throw new Error('The native Claude result has no model usage for the requested model.')
  expect(result.modelUsage[request.body.model]).toMatchObject({ contextWindow: 1_000_000 })
}

/**
 * The name of the opus[1m] row in the native catalog.
 * Claude Code 2.1.289 drops "(1M context)" from the Opus row, because the 1M window is the only Opus window that it offers.
 * The native result proves the 1M window: see expectNativeOpus1M.
 */
const OPUS_1M_LABEL = 'Opus'

test.describe('Agent Settings', () => {
  test('switch model', async ({ native, page }) => {
    const trigger = settingsBar(page)
    await expect(trigger).toBeVisible()

    await exerciseNativeOption(native, {
      groupId: 'model',
      value: 'haiku',
      nativeProof: request => expect(request.body).toMatchObject({
        model: expect.stringMatching(/^claude-haiku-/),
      }),
    })
    await expectSettingsChip(page, 'Haiku')
  })

  test.describe('bracketed model names', () => {
    test('switch to model with bracket characters', async ({ authenticatedWorkspace, native, page, modelScript }) => {
      const trigger = settingsBar(page)
      await expect(trigger).toBeVisible()

      // The bracketed model ID must survive shell quoting during the native restart.
      await chooseSettingsOption(page, 'model-opus[1m]')
      await expectSettingsChip(page, OPUS_1M_LABEL)
      await waitForSettingsIdle(page)

      // The actual answer proves that the restarted native process serves a turn.
      const step = await modelScript.queue({ text: '7' })
      await sendMessage(page, modelScript.prompt('What is 3+4? Reply with just the number, nothing else.'))
      await expectNativeOpus1M(native, await modelScript.requestAt(step))

      // The duration row also has the agent role. Search all visible answer rows.
      await expectAssistantAnswer(page, { answer: /\b7\b/ })
    })
  })

  test('model persistence across refresh', async ({ native, page }) => {
    const trigger = settingsBar(page)
    await expect(trigger).toBeVisible()

    // Change model to Haiku (default is Sonnet, dropdown auto-closes on select)
    await chooseSettingsOption(page, 'model-haiku')
    await expectSettingsChip(page, 'Haiku')

    // Wait for the Worker to confirm the model change.
    await waitForSettingsIdle(page)

    // Refresh the page
    await page.reload()

    // Verify Haiku is still selected after refresh
    const triggerAfter = settingsBar(page)
    await expect(triggerAfter).toBeVisible()
    await expectSettingsChip(page, 'Haiku')
    const restored = await sendNativeAnswer(native, 'Reply once with the restored Haiku model.', 'The restored Haiku model answered.')
    expect(restored.protocol).toBe('anthropic-messages')
    expect(restored.body).toMatchObject({
      model: expect.stringMatching(/^claude-haiku-/),
    })
  })

  test('model/effort items not disabled when idle', async ({ authenticatedWorkspace, page }) => {
    const trigger = settingsBar(page)
    await expect(trigger).toBeVisible()

    // The CLI supplies the available options. Check each offered item without assuming a fixed model catalog.
    for (const group of ['model', 'effort', 'permissionMode']) {
      const menu = await openSettingsMenu(page, group)
      const items = menu.getByRole('menuitemradio')
      await expect(items).not.toHaveCount(0)
      for (const item of await items.all())
        await expect(item).not.toHaveAttribute('data-disabled', '')
      await expect(page.getByTestId('settings-disabled-footnote')).not.toBeVisible()
    }

    await page.keyboard.press('Escape')
  })

  test('settings change notification appears in chat', async ({ authenticatedWorkspace, page }) => {
    const trigger = settingsBar(page)
    await expect(trigger).toBeVisible()

    // Change model to Haiku (default is Sonnet) — should produce a notification
    await chooseSettingsOption(page, 'model-haiku')
    await expectSettingsChip(page, 'Haiku')

    // Verify the notification bubble appears in chat
    await expect(visibleOnly(page.getByText('Model (Sonnet \u2192 Haiku)'))).toBeVisible()
  })
})

// Closed composer menus retain model labels. Match the notification's exact shape.
const MODEL_CHANGE_PATTERN = /Model \(Sonnet → Opus\)/

claudeTest.describe('1m-context model', () => {
  claudeTest('switch to opus[1m] and exchange messages', async ({ authenticatedWorkspace, native, page, modelScript }) => {
    const trigger = settingsBar(page)
    await expect(trigger).toBeVisible()

    // The isolated default model is Sonnet.
    await expectSettingsChip(page, 'Sonnet')

    // Switch to Opus[1m]
    await chooseSettingsOption(page, 'model-opus[1m]')
    await expectSettingsChip(page, OPUS_1M_LABEL)

    // Keep the actual model-change notification. ChatView keeps a hidden premeasure copy of each
    // unmeasured row, so the text query needs the visible filter or it can match both copies.
    await expect(visibleOnly(page.getByText(MODEL_CHANGE_PATTERN))).toBeVisible()

    // Wait for agent restart to complete
    await waitForSettingsIdle(page)

    // Verify the native model and reported 1M window in the restarted process's next request.
    const first = await modelScript.queue({ text: '8' })
    await sendMessage(page, modelScript.prompt('What is 5+3? Reply with just the number, nothing else.'))
    await expectNativeOpus1M(native, await modelScript.requestAt(first))

    // The duration row also has the agent role. Search all visible answer rows.
    await expectAssistantAnswer(page, { answer: /\b8\b/ })

    // Send a follow-up to confirm the agent session is stable
    const next = await modelScript.queue({ text: '6' })
    await sendMessage(page, modelScript.prompt('What is 10-4? Reply with just the number, nothing else.'))
    await expectNativeOpus1M(native, await modelScript.requestAt(next))

    await expectAssistantAnswer(page, { answer: /\b6\b/ })

    // Verify the model is still shown as Opus[1m] after exchanging messages
    await expectSettingsChip(page, OPUS_1M_LABEL)
  })

  // The Default selection must resolve to a concrete model and retain its effort menu.
  // The resumed native session can retain Opus. Do not require the account's initial model.
  claudeTest('switching to Default resolves to a concrete model with its effort menu', async ({ authenticatedWorkspace, page }) => {
    const trigger = settingsBar(page)
    await expect(trigger).toBeVisible()

    // A fresh tab starts on the account default, which resolves to Sonnet.
    await expectSettingsChip(page, 'Sonnet')

    // Move off the default onto a concrete non-default model.
    await chooseSettingsOption(page, 'model-opus[1m]')
    await expectSettingsChip(page, OPUS_1M_LABEL)
    await waitForSettingsIdle(page)

    // The Worker relaunches without --model. The CLI resolves the session's concrete model.
    await chooseSettingsOption(page, 'model-default')
    await waitForSettingsIdle(page)

    // A concrete model must replace the Default placeholder.
    await expectNoSettingsChip(page, 'Default (recommended)')

    // The concrete model must also restore its offered effort group.
    const effort = await openSettingsMenu(page, 'effort')
    await expect(effort.getByTestId('effort-high')).toBeVisible()
  })
})
