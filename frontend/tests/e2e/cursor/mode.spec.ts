import { expect } from '@playwright/test'
import { cursorTest } from '../cursor-fixtures'
import { sendNativeAnswer } from '../helpers/nativeConversation'
import { exerciseRestoredNativeOption } from '../helpers/nativeSettings'
import { thinkingIndicatorShownDuring } from '../helpers/thinkingIndicatorWatch'
import { chooseSettingsOption, expectSettingsChip, offeredSettingsOptions, visibleOnly, waitForNativeSettingsHydrated, waitForSettingsIdle } from '../helpers/ui'
import { exerciseCursorNativePlanRPC, exerciseCursorSelectedPlanSettings } from './settingsScenario'

cursorTest('mode: keeps a selected model and Plan mode after a turn and reload', async ({ native }) => {
  await exerciseCursorSelectedPlanSettings(native, 'permissionMode')
})

cursorTest('confirms Plan mode through the native set-mode RPC', async ({ native }) => {
  await exerciseCursorNativePlanRPC(native)
})

cursorTest('returns from Plan and Ask to Agent with one notice per native mode change', async ({ native }) => {
  const { page, modelScript } = native
  await waitForNativeSettingsHydrated(page)
  expect(await offeredSettingsOptions(page, 'permissionMode')).toEqual(['agent', 'plan', 'ask'])
  await expectSettingsChip(page, 'Agent')

  for (const change of [
    { value: 'plan', label: 'Plan', previous: 'Agent', mode: 3 },
    { value: 'ask', label: 'Ask', previous: 'Plan', mode: 2 },
    { value: 'agent', label: 'Agent', previous: 'Ask', mode: 1 },
  ]) {
    const before = (await modelScript.status()).requests.length
    const shown = await thinkingIndicatorShownDuring(page, async () => {
      await chooseSettingsOption(page, `permissionMode-${change.value}`)
      await waitForSettingsIdle(page)
      await expectSettingsChip(page, change.label)
    })
    expect(shown).toBe(false)
    expect((await modelScript.status()).requests).toHaveLength(before)
    const notice = visibleOnly(page.getByText(`Mode (${change.previous} → ${change.label})`, { exact: true }))
    await expect(notice).toHaveCount(1)
    const request = await sendNativeAnswer(native, `Reply in ${change.label} mode.`, `The ${change.label} mode reached the native turn.`)
    expect(request.nativeRequest).toMatchObject({ mode: change.mode })
    await expect(notice).toHaveCount(1)
  }

  await exerciseRestoredNativeOption(native, {
    groupId: 'permissionMode',
    value: 'agent',
    nativeProof: request => expect(request.nativeRequest).toMatchObject({ mode: 1 }),
  })
  await expectSettingsChip(page, 'Agent')
  for (const notice of ['Mode (Agent → Plan)', 'Mode (Plan → Ask)', 'Mode (Ask → Agent)'])
    await expect(visibleOnly(page.getByText(notice, { exact: true }))).toHaveCount(1)
})
