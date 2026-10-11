import { expect } from '@playwright/test'
import { clineTest } from '../cline-fixtures'
import { MOCK_MODELS } from '../helpers/mockAgentEnvironment'
import { sendNativeAnswer } from '../helpers/nativeConversation'
import { exerciseEffortModelRoundTrip, exerciseModelSwitchKeepsOption, exerciseNativeOption } from '../helpers/nativeSettings'
import { chooseSettingsOption, expectSettingsChip, waitForSettingsHydrated, waitForSettingsIdle } from '../helpers/ui'
import { nativeContext } from './scenarios'

/**
 * The selected effort must reach an actual native model request.
 *
 * Cline exposes native reasoning effort for catalog models that support it.
 * The configured custom model exposes no effort ladder.
 */
clineTest.describe('Cline settings', () => {
  clineTest('applies reasoning effort to the native model request', async ({ askingClineWorkspace, page, modelScript, leapmuxServer }) => {
    const context = await nativeContext({ page, modelScript, leapmuxServer, workspaceId: askingClineWorkspace.workspaceId })
    await exerciseNativeOption(context, {
      groupId: 'effort',
      value: 'high',
      prepare: async () => {
        await waitForSettingsHydrated(page)
        await chooseSettingsOption(page, 'model-deepseek-v4-pro')
        await waitForSettingsIdle(page)
        // A turn at the default effort states no effort, so the selected effort below is a change.
        const baseline = await sendNativeAnswer(context, 'Reply once at default effort.', 'Default effort answered.')
        expect(baseline.body).not.toHaveProperty('reasoning_effort')
      },
      nativeProof: request => expect(request.body).toMatchObject({ model: 'deepseek-v4-pro', reasoning_effort: 'high' }),
    })
    await expectSettingsChip(page, 'High')
  })
})

clineTest.describe('Cline model switch', () => {
  // Both catalog models offer high. Cline merges each field of a connection update alone, so a model switch keeps the effort.
  clineTest('keeps the chosen effort after a model switch and a reload', async ({ askingClineWorkspace, page, modelScript, leapmuxServer }) => {
    const context = await nativeContext({ page, modelScript, leapmuxServer, workspaceId: askingClineWorkspace.workspaceId })
    await exerciseModelSwitchKeepsOption(context, {
      prepare: async () => {
        await waitForSettingsHydrated(page)
        await chooseSettingsOption(page, 'model-deepseek-v4-pro')
        await waitForSettingsIdle(page)
      },
      kept: { groupId: 'effort', value: 'high' },
      model: 'deepseek-flash',
      nativeProof: (request) => {
        expect(request.body).toMatchObject({ model: 'deepseek-flash', reasoning_effort: 'high' })
      },
    })
  })

  // The configured custom model has no effort ladder (`cline/catalog.go`), so it offers no effort. The Worker sends
  // Auto with the switch to it and with the switch back (`resetEffortToAutoIfUnsupported`, Cline manages its effort).
  // Auto sends no reasoning setting (`clineAutoEffort`), so the next request states no effort.
  clineTest('hides the effort for a model without levels and settles Auto after the round trip', async ({ askingClineWorkspace, page, modelScript, leapmuxServer }) => {
    const context = await nativeContext({ page, modelScript, leapmuxServer, workspaceId: askingClineWorkspace.workspaceId })
    await exerciseEffortModelRoundTrip(context, {
      effortGroupId: 'effort',
      model: 'deepseek-v4-pro',
      chosen: 'high',
      via: MOCK_MODELS.cline,
      viaEfforts: 'hidden',
      settled: 'auto',
      nativeProof: (request) => {
        expect(request.body).toMatchObject({ model: 'deepseek-v4-pro' })
        expect(request.body).not.toHaveProperty('reasoning_effort')
      },
    })
  })
})
