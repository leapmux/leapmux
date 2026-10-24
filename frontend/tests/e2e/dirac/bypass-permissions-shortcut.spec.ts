import { expect } from '@playwright/test'
import { DIRAC_CONFIG } from '../../../src/generated/contracts/dirac-protocol'
import { DIRAC_E2E_SKIP_REASON, diracTest } from '../dirac-fixtures'
import { exerciseBypassPermissions } from '../helpers/nativeBypassPermissions'
import { exerciseMidTurnBypass } from '../helpers/nativeMidTurnBypass'
import { exerciseNativePermissionDecision, exerciseNativeToolWrite } from '../helpers/nativePermission'
import { currentNativeAgent, expectNativeOptionValue } from '../helpers/nativeScenario'
import { exerciseWorkerRestart } from '../helpers/nativeWorkerRestart'
import { bashToolCall } from '../helpers/providerToolCalls'
import { applyPermissionPreset, chooseSettingsOption, waitForSettingsIdle } from '../helpers/ui'
import { createProviderProcessTest } from '../provider-fixture-factory'
import { DIRAC_AGENT, nativeContext } from './scenarios'

diracTest('runs native tools under Bypass after reload and restores approval when YOLO is off', async ({ askingDiracWorkspace, page, modelScript, leapmuxServer }) => {
  const native = await nativeContext({ page, modelScript, leapmuxServer, workspaceId: askingDiracWorkspace.workspaceId })
  await expectNativeOptionValue(native, DIRAC_CONFIG.Yolo, 'false')
  await expectNativeOptionValue(native, 'auto_approve', 'false')
  const before = await currentNativeAgent(native)
  const unchanged = before.optionGroups.filter(group => ['model', 'permissionMode', 'reasoning_effort', 'auto_approve'].includes(group.id))
  await exerciseBypassPermissions(native, {
    settingsProof: (agent) => {
      expect(agent.optionGroups.find(group => group.id === DIRAC_CONFIG.Yolo)?.currentValue).toBe('true')
      for (const group of unchanged)
        expect(agent.optionGroups.find(current => current.id === group.id)?.currentValue, group.id).toBe(group.currentValue)
    },
  })
  await chooseSettingsOption(page, `${DIRAC_CONFIG.Yolo}-false`)
  await waitForSettingsIdle(page)
  await expectNativeOptionValue(native, DIRAC_CONFIG.Yolo, 'false')
  await expectNativeOptionValue(native, 'auto_approve', 'false')
  await exerciseNativePermissionDecision(native, {
    toolCall: bashToolCall(native.provider, 'dirac-bypass-disabled', 'echo "DIRAC_BYPASS_DISABLED_$(printf 42)"'),
    decision: 'deny',
    nativeProof: request => expect(JSON.stringify(request.body)).not.toContain('DIRAC_BYPASS_DISABLED_42'),
  })
})

diracTest('approves the pending native tool and the next tool when Bypass changes during a turn', async ({ askingDiracWorkspace, page, modelScript, leapmuxServer }) => {
  const native = await nativeContext({ page, modelScript, leapmuxServer, workspaceId: askingDiracWorkspace.workspaceId })
  await expectNativeOptionValue(native, DIRAC_CONFIG.Yolo, 'false')
  await expectNativeOptionValue(native, 'auto_approve', 'false')
  await exerciseMidTurnBypass(native)
  await expectNativeOptionValue(native, DIRAC_CONFIG.Yolo, 'true')
})

const restartTest = createProviderProcessTest({ agent: DIRAC_AGENT, nativeContext, skipReason: DIRAC_E2E_SKIP_REASON })

restartTest('restores native Bypass after a worker restart and executes a tool without approval', async ({ native, separateHubWorker }) => {
  await applyPermissionPreset(native.page, 'bypass')
  await waitForSettingsIdle(native.page)
  await expectNativeOptionValue(native, DIRAC_CONFIG.Yolo, 'true')
  await exerciseWorkerRestart(native, separateHubWorker, 'worker')
  await expectNativeOptionValue(native, DIRAC_CONFIG.Yolo, 'true')
  await exerciseNativeToolWrite(native, { permission: 'absent' })
})
