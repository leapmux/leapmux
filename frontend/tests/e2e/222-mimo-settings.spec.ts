import type { MockModelRequestRecord } from './helpers/mockModelScript'
import { existsSync, writeFileSync } from 'node:fs'
import { join } from 'node:path'
import { MIMO_OPTION, MIMO_PERMISSION_POLICY } from '../../src/generated/contracts/mimo-protocol'
import { AgentProvider } from '../../src/generated/proto/leapmux/v1/agent_pb'
import { agentOpenOptions, agentSettings } from './agentSettings'
import { openAgentViaAPI } from './helpers/api'
import { MOCK_MODELS, MOCK_PROVIDER_IDS } from './helpers/mockAgentEnvironment'
import { bashToolCall } from './helpers/providerToolCalls'
import { createTestDirectory } from './helpers/runDirectory'
import { applyPermissionPreset, chooseSettingsOption, closeComposerMenus, expectSettingsChip, openPlusMenu, openSettingsMenu, openWorkspace, sendMessage, waitForAgentIdle, waitForSettingsHydrated, waitForSettingsIdle } from './helpers/ui'
import { expect, MIMO_E2E_SKIP_REASON, mimoTest } from './mimo-fixtures'

mimoTest.skip(!!MIMO_E2E_SKIP_REASON, MIMO_E2E_SKIP_REASON || '')

/** The body of the last model request that a step of this script answered. */
function lastStepBody(requests: MockModelRequestRecord[]): Record<string, unknown> {
  const last = requests.filter(request => request.stepIndex !== undefined).at(-1)
  if (!last || typeof last.body !== 'object' || last.body === null)
    throw new Error('no scripted step answered a model request')
  return { ...last.body }
}

mimoTest.describe('MiMo Code settings', () => {
  // Each setting is read off the next model request, which is where MiMo applies
  // it: the model id, the reasoning variant and the primary agent's prompt.
  mimoTest('switches the model, the effort and the mode for the next prompt', async ({ authenticatedMiMoWorkspace, page, modelScript }) => {
    void authenticatedMiMoWorkspace
    await waitForSettingsHydrated(page)
    await expectSettingsChip(page, 'Build')

    await chooseSettingsOption(page, `model-${MOCK_PROVIDER_IDS.openCode}/${MOCK_MODELS.pi}`)
    await waitForSettingsIdle(page)
    await chooseSettingsOption(page, 'effort-low')
    await waitForSettingsIdle(page)
    await expectSettingsChip(page, 'Low')
    await chooseSettingsOption(page, 'permissionMode-plan')
    await waitForSettingsIdle(page)
    await expectSettingsChip(page, 'Plan')

    await modelScript.queue({ text: 'SETTINGS_APPLIED' })
    await sendMessage(page, modelScript.prompt('Describe the plan in one word.'))
    await modelScript.waitForSteps()
    await waitForAgentIdle(page, 120_000)

    const body = lastStepBody((await modelScript.status()).requests)
    expect(body.model).toBe(MOCK_MODELS.pi)
    expect(body.reasoning_effort).toBe('low')
    expect(JSON.stringify(body.messages)).toMatch(/plan mode/i)

    await page.reload()
    await waitForSettingsHydrated(page)
    await expectSettingsChip(page, 'Plan')
    await expectSettingsChip(page, 'Low')
  })

  // MiMo has no permission mode. Bypass turns on both of its runtime switches,
  // and the second one is what lets a deletion run without the question that
  // MiMo otherwise always asks for it.
  mimoTest('bypass runs a deletion without asking', async ({ page, authenticatedEmptyWorkspace, leapmuxServer, modelScript }) => {
    const directory = createTestDirectory('mimo-bypass-')
    const file = join(directory, 'doomed.txt')
    writeFileSync(file, 'delete me\n')
    await openAgentViaAPI(leapmuxServer.hubUrl, leapmuxServer.adminToken, leapmuxServer.workerId, authenticatedEmptyWorkspace.workspaceId, directory, {
      agentProvider: AgentProvider.MIMO_CODE,
      ...agentOpenOptions(agentSettings(AgentProvider.MIMO_CODE)),
    })
    await openWorkspace(page, authenticatedEmptyWorkspace.workspaceId)
    await waitForSettingsHydrated(page)

    const menu = await openPlusMenu(page)
    await expect(menu.getByTestId('composer-smart-permissions')).toHaveCount(0)
    await expect(menu.getByTestId('composer-bypass-permissions')).toBeVisible()
    await page.keyboard.press('Escape')
    await applyPermissionPreset(page, 'bypass')
    // The policy is not a status-bar axis, so its own group in the menu states it.
    const policies = await openSettingsMenu(page, MIMO_OPTION.PermissionPolicy)
    await expect(policies.getByTestId(`${MIMO_OPTION.PermissionPolicy}-${MIMO_PERMISSION_POLICY.Bypass}`)).toHaveAttribute('aria-checked', 'true')
    await closeComposerMenus(page)

    await modelScript.queue(
      { toolCalls: [bashToolCall(AgentProvider.MIMO_CODE, 'delete-call', 'rm -f doomed.txt')] },
      { text: 'DELETED_WITHOUT_ASKING' },
    )
    await sendMessage(page, modelScript.prompt('Delete doomed.txt.'))
    await modelScript.waitForSteps()
    await waitForAgentIdle(page, 120_000)
    await expect(page.locator('[data-testid="control-banner"]')).toHaveCount(0)
    expect(existsSync(file)).toBe(false)
  })
})
