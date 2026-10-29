import { expect } from '@playwright/test'
import { FASTAGENT_OPTION } from '../../../src/generated/contracts/fastagent-protocol'
import { FAST_AGENT_E2E_SKIP_REASON, fastAgentTest } from '../fastagent-fixtures'
import { exerciseBypassPermissions } from '../helpers/nativeBypassPermissions'
import { exerciseNativePermissionDecision, exerciseNativeToolWrite } from '../helpers/nativePermission'
import { currentNativeAgent, expectNativeOptionValue } from '../helpers/nativeScenario'
import { exerciseWorkerRestart } from '../helpers/nativeWorkerRestart'
import { bashToolCall } from '../helpers/providerToolCalls'
import { chooseSettingsOption, closeComposerMenus, openSettingsMenu, waitForSettingsIdle } from '../helpers/ui'
import { createProviderProcessTest } from '../provider-fixture-factory'
import { FAST_AGENT_AGENT, nativeContext } from './scenarios'

fastAgentTest('applies native Bypass before the first message, retains it on reload, and restores permission requests', async ({ native }) => {
  const { page } = native
  await expectNativeOptionValue(native, FASTAGENT_OPTION.BypassPermissions, 'false')
  const menu = await openSettingsMenu(page, FASTAGENT_OPTION.BypassPermissions)
  await menu.getByTestId(`${FASTAGENT_OPTION.BypassPermissions}-true`).hover()
  await expect(page.getByRole('tooltip').filter({ visible: true })).toContainText('restarts the agent and stops any active turn')
  await closeComposerMenus(page)
  let sessionId: string | undefined
  await exerciseBypassPermissions(native, {
    settingsProof: (agent) => {
      expect(agent.optionGroups.find(group => group.id === FASTAGENT_OPTION.BypassPermissions)?.currentValue).toBe('true')
      expect(agent.agentSessionId).not.toBe('')
      sessionId ??= agent.agentSessionId
      expect(agent.agentSessionId).toBe(sessionId)
    },
  })
  await chooseSettingsOption(page, `${FASTAGENT_OPTION.BypassPermissions}-false`)
  await waitForSettingsIdle(page)
  await expectNativeOptionValue(native, FASTAGENT_OPTION.BypassPermissions, 'false')
  expect((await currentNativeAgent(native)).agentSessionId).toBe(sessionId)
  await exerciseNativePermissionDecision(native, {
    toolCall: bashToolCall(native.provider, 'fastagent-bypass-disabled', 'echo "FASTAGENT_BYPASS_DISABLED_$(printf 42)"'),
    decision: 'deny',
    nativeProof: request => expect(JSON.stringify(request.body)).not.toContain('FASTAGENT_BYPASS_DISABLED_42'),
  })
})

const restartTest = createProviderProcessTest({
  agent: FAST_AGENT_AGENT,
  nativeContext,
  skipReason: FAST_AGENT_E2E_SKIP_REASON,
  openOptions: {
    optionValues: { [FASTAGENT_OPTION.BypassPermissions]: 'true' },
  },
})

restartTest('retains the native Bypass flag after worker restart and restores normal permission handling afterward', async ({ native, separateHubWorker }) => {
  await expectNativeOptionValue(native, FASTAGENT_OPTION.BypassPermissions, 'true')
  await exerciseWorkerRestart(native, separateHubWorker, 'worker')
  await expectNativeOptionValue(native, FASTAGENT_OPTION.BypassPermissions, 'true')
  await exerciseNativeToolWrite(native, { permission: 'absent' })
  const before = await currentNativeAgent(native)
  await chooseSettingsOption(native.page, `${FASTAGENT_OPTION.BypassPermissions}-false`)
  await waitForSettingsIdle(native.page)
  await expectNativeOptionValue(native, FASTAGENT_OPTION.BypassPermissions, 'false')
  expect((await currentNativeAgent(native)).agentSessionId).toBe(before.agentSessionId)
  await exerciseNativeToolWrite(native, { permission: 'native' })
})
