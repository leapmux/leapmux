import { expect } from '@playwright/test'
import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { exerciseNativePermissionDecision } from '../helpers/nativePermission'
import { nativeModelContextText } from '../helpers/nativeScenario'
import { bashToolCall } from '../helpers/providerToolCalls'
import { chooseSettingsOption, expectSettingsOptionChosen, waitForSettingsHydrated, waitForSettingsIdle } from '../helpers/ui'
import { OH_MY_PI_E2E_SKIP_REASON, ohMyPiTest } from '../ohmypi-fixtures'

ohMyPiTest.skip(!!OH_MY_PI_E2E_SKIP_REASON, OH_MY_PI_E2E_SKIP_REASON || '')

ohMyPiTest('applies the native approval mode through a restart and a reload', async ({ authenticatedOhMyPiWorkspace, page, modelScript, leapmuxServer }) => {
  const context = { page, modelScript, leapmuxServer, workspaceId: authenticatedOhMyPiWorkspace.workspaceId, provider: AgentProvider.OH_MY_PI }
  await chooseSettingsOption(page, 'permissionMode-always-ask')
  await waitForSettingsIdle(page)
  for (const reload of [false, true]) {
    if (reload) {
      await page.reload()
      await waitForSettingsHydrated(page)
    }
    await expectSettingsOptionChosen(page, 'permissionMode-always-ask')
    await exerciseNativePermissionDecision(context, {
      toolCall: bashToolCall(AgentProvider.OH_MY_PI, `native-mode-${reload}`, 'printf "NATIVEAPPROVAL%s\\n" "$((40 + 2))"'),
      decision: 'allow',
      beforeDecision: banner => expect(banner).toContainText('NATIVEAPPROVAL'),
      nativeProof: request => expect(nativeModelContextText(request)).toContain('NATIVEAPPROVAL42'),
    })
  }
})
