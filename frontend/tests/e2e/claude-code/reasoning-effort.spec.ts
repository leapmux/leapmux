import type { Page } from '@playwright/test'
import type { ModelScript } from '../helpers/modelScriptFixture'
import type { ManagedNativeScenarioContext } from '../helpers/nativeScenario'
import type { SeparateServerInfo } from '../process-control-fixtures'
import { expect } from '@playwright/test'
import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { claudeProcessTest as test } from '../claude-fixtures'
import { sendNativeAnswer } from '../helpers/nativeConversation'
import { exerciseModelSwitchKeepsOption, exerciseNativeOption } from '../helpers/nativeSettings'
import { chooseSettingsOption, expectSettingsChip, expectSettingsOptionChosen, offeredSettingsOptions, openPlusMenu, openSettingsMenu, settingsBar, settingsGroupTrigger, waitForSettingsHydrated, waitForSettingsIdle } from '../helpers/ui'
import { claudeUltracodeEnabled } from './ultracodeRequest'

/** The scenario context of the Claude Code agent that the separate Hub and Worker run. */
function separateHubContext(fixtures: { page: Page, modelScript: ModelScript, separateHubWorker: SeparateServerInfo, authenticatedWorkspace: { workspaceId: string } }): ManagedNativeScenarioContext {
  return {
    page: fixtures.page,
    modelScript: fixtures.modelScript,
    leapmuxServer: fixtures.separateHubWorker,
    provider: AgentProvider.CLAUDE_CODE,
    workspaceId: fixtures.authenticatedWorkspace.workspaceId,
  }
}

test.describe('Agent Settings', () => {
  test('sends the selected effort in the next native request', async ({ authenticatedWorkspace, separateHubWorker, page, modelScript }) => {
    await expect(settingsBar(page)).toBeVisible()
    await exerciseNativeOption(separateHubContext({ page, modelScript, separateHubWorker, authenticatedWorkspace }), {
      groupId: 'effort',
      value: 'high',
      nativeProof: request => expect(request.body).toMatchObject({ output_config: { effort: 'high' } }),
    })
  })

  test('effort hidden when haiku selected', async ({ authenticatedWorkspace, page }) => {
    const trigger = settingsBar(page)
    await expect(trigger).toBeVisible()

    // Haiku offers no effort group or effort chip. Sonnet offers both controls.
    const effortSubmenu = settingsGroupTrigger(page, 'effort')
    const effortChip = page.locator('[data-testid="composer-effort-trigger"]')

    await expect((await openSettingsMenu(page, 'effort')).getByTestId('effort-high')).toBeVisible()
    await chooseSettingsOption(page, 'model-haiku')
    await expectSettingsChip(page, 'Haiku')
    await waitForSettingsIdle(page)

    // Effort is hidden for Haiku, on both surfaces.
    await openPlusMenu(page)
    await expect(effortSubmenu).toHaveCount(0)
    await expect(effortChip).toHaveCount(0)
    await page.keyboard.press('Escape')

    // Restore Sonnet and its effort group.
    await chooseSettingsOption(page, 'model-sonnet')
    await expectSettingsChip(page, 'Sonnet')
    await waitForSettingsIdle(page)

    await expect((await openSettingsMenu(page, 'effort')).getByTestId('effort-high')).toBeVisible()
    await page.keyboard.press('Escape')
  })

  test('the effort menu is the same before and after a model round trip', async ({ authenticatedWorkspace, page }) => {
    void authenticatedWorkspace // fixture trigger
    const trigger = settingsBar(page)
    await expect(trigger).toBeVisible()

    // The static and live catalogs must offer the same effort levels for capable models.
    // The old fallback offered Max alone, then the live catalog added Xhigh after a model change.
    // Compare the original and restored menus to prevent that change.
    const effortOptions = async (): Promise<string[]> => {
      await waitForSettingsHydrated(page)
      const values = await offeredSettingsOptions(page, 'effort')
      expect(values, 'a capable model offers the automatic effort').toContain('auto')
      return values
    }

    const onSonnet = await effortOptions()
    expect(onSonnet, 'Sonnet offers an effort menu').not.toHaveLength(0)

    await chooseSettingsOption(page, 'model-opus[1m]')
    await expectSettingsChip(page, 'Opus')
    await waitForSettingsIdle(page)
    expect(await effortOptions(), 'Opus offers the same tiers').toEqual(onSonnet)

    await chooseSettingsOption(page, 'model-sonnet')
    await expectSettingsChip(page, 'Sonnet')
    await waitForSettingsIdle(page)
    expect(await effortOptions(), 'and Sonnet still does on the way back').toEqual(onSonnet)
  })

  test('ultracode effort is selectable and keeps the agent working', async ({ authenticatedWorkspace, separateHubWorker, page, modelScript }) => {
    await expect(settingsBar(page)).toBeVisible()
    // The isolated native CLI supports Ultracode and applies xhigh with its harness instruction.
    await exerciseNativeOption(separateHubContext({ page, modelScript, separateHubWorker, authenticatedWorkspace }), {
      groupId: 'effort',
      value: 'ultracode',
      prepare: async () => {
        await chooseSettingsOption(page, 'model-sonnet')
        await waitForSettingsIdle(page)
      },
      nativeProof: (request) => {
        expect(request.protocol).toBe('anthropic-messages')
        expect(request.body).toMatchObject({ model: expect.stringMatching(/^claude-sonnet-/), output_config: { effort: 'xhigh' } })
        expect(claudeUltracodeEnabled(request)).toBe(true)
      },
    })
  })

  // The user changes the model alone, so the switch carries no effort. The stored tier stays when
  // the new model offers it. Opus and Sonnet both offer xhigh in the installed CLI.
  test('a model switch keeps an effort that the new model supports', async ({ authenticatedWorkspace, separateHubWorker, page, modelScript }) => {
    await expect(settingsBar(page)).toBeVisible()
    await exerciseModelSwitchKeepsOption(separateHubContext({ page, modelScript, separateHubWorker, authenticatedWorkspace }), {
      prepare: async () => {
        await chooseSettingsOption(page, 'model-opus[1m]')
        await expectSettingsChip(page, 'Opus')
        await waitForSettingsIdle(page)
      },
      kept: { groupId: 'effort', value: 'xhigh' },
      model: 'sonnet',
      nativeProof: request => expect(request.body).toMatchObject({
        model: expect.stringMatching(/^claude-sonnet-/),
        output_config: { effort: 'xhigh' },
      }),
    })
  })

  // Haiku offers no effort axis, so the switch to Haiku drops the tier. The switch back to Sonnet
  // carries no effort either, and the row holds none, so Sonnet reports the level that it selects.
  test('a model switch to a model without effort resets the effort', async ({ authenticatedWorkspace, page, modelScript }) => {
    void authenticatedWorkspace // fixture trigger
    const trigger = settingsBar(page)
    await expect(trigger).toBeVisible()

    await chooseSettingsOption(page, 'model-sonnet')
    await expectSettingsChip(page, 'Sonnet')
    await waitForSettingsIdle(page)
    await chooseSettingsOption(page, 'effort-xhigh')
    await waitForSettingsIdle(page)
    await expectSettingsOptionChosen(page, 'effort-xhigh')

    await chooseSettingsOption(page, 'model-haiku')
    await expectSettingsChip(page, 'Haiku')
    await waitForSettingsIdle(page)
    await chooseSettingsOption(page, 'model-sonnet')
    await expectSettingsChip(page, 'Sonnet')
    await waitForSettingsIdle(page)

    // The effort menu chooses one level, so Medium also proves that Xhigh is gone.
    await expectSettingsOptionChosen(page, 'effort-medium')

    const request = await sendNativeAnswer({ page, modelScript, provider: AgentProvider.CLAUDE_CODE }, 'Reply once after the round trip through Haiku.', 'Claude answered after the round trip.')
    expect(request.body).toMatchObject({ output_config: { effort: 'medium' } })
  })

  // A new session pins no effort: the CLI chooses the level of its model. The menu shows that level, and a
  // model switch must keep it. The CLI would otherwise choose the default of the new model, and the user
  // would see the effort change although only the model changed.
  test('a model switch keeps the level that the CLI chose for an automatic session', async ({ authenticatedWorkspace, page, modelScript }) => {
    void authenticatedWorkspace // fixture trigger
    const trigger = settingsBar(page)
    await expect(trigger).toBeVisible()

    await chooseSettingsOption(page, 'model-sonnet')
    await expectSettingsChip(page, 'Sonnet')
    await waitForSettingsIdle(page)
    // Auto restarts the agent without --effort. Sonnet then runs at its own default, Medium.
    await chooseSettingsOption(page, 'effort-auto')
    await waitForSettingsIdle(page)
    await expectSettingsOptionChosen(page, 'effort-medium')

    // Fable defaults to High, so a switch that keeps Medium differs from a switch that takes the default.
    // The effort menu chooses one level, so Medium also proves that High is not chosen.
    await chooseSettingsOption(page, 'model-fable[1m]')
    await expectSettingsChip(page, 'Fable')
    await waitForSettingsIdle(page)
    await expectSettingsOptionChosen(page, 'effort-medium')

    const request = await sendNativeAnswer({ page, modelScript, provider: AgentProvider.CLAUDE_CODE }, 'Reply once after the switch to Fable.', 'Claude answered on Fable.')
    expect(request.body).toMatchObject({
      model: expect.stringMatching(/^claude-fable-/),
      output_config: { effort: 'medium' },
    })
  })
})
