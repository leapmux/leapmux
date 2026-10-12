import { existsSync } from 'node:fs'
import { join } from 'node:path'
import { expect } from '@playwright/test'
import { createNativePermissionFileWrite, exerciseNativePermissionDecision } from '../helpers/nativePermission'
import { currentNativeAgent } from '../helpers/nativeScenario'
import { nativeToolResultContent } from '../helpers/nativeToolResult'
import { chooseSettingsOption, expectNoControlBanner, expectSettingsOptionChosen, waitForNativeSettingsHydrated, waitForSettingsIdle } from '../helpers/ui'
import { museTest } from '../muse-fixtures'

for (const decision of ['allow', 'deny'] as const) {
  museTest(`returns the native ${decision} choice before the requested file can change`, async ({ native }) => {
    await waitForNativeSettingsHydrated(native.page)
    await chooseSettingsOption(native.page, 'permissionMode-promptUnmatched')
    await waitForSettingsIdle(native.page)
    await expectSettingsOptionChosen(native.page, 'permissionMode-promptUnmatched')
    const agent = await currentNativeAgent(native)
    const fileName = `muse-permission-${decision}.txt`
    const file = join(agent.workingDir, fileName)
    const callId = `muse-permission-${decision}`
    const operation = await createNativePermissionFileWrite(native, {
      fileName,
      callId,
      outputPrefix: `MUSE_PERMISSION_${decision.toUpperCase()}_`,
    })
    const request = await exerciseNativePermissionDecision(native, {
      toolCall: operation.toolCall,
      decision,
      beforeDecision: async (banner) => {
        await expect(banner).toBeVisible()
        await operation.beforeDecision()
      },
      nativeProof: async (resultRequest) => {
        expect(resultRequest.mockCredential?.accepted).toBe(true)
        if (decision === 'allow') {
          await operation.nativeProof(resultRequest)
        }
        else {
          expect(existsSync(file)).toBe(false)
          expect(nativeToolResultContent(resultRequest, callId)).toBe('tool denied: approval aborted')
        }
      },
      viewProof: () => expectNoControlBanner(native.page),
    })
    expect(request.protocol).toBe('openai-responses')
    await native.page.reload()
    await waitForNativeSettingsHydrated(native.page)
    await expectSettingsOptionChosen(native.page, 'permissionMode-promptUnmatched')
    await expectNoControlBanner(native.page)
    if (decision === 'deny')
      expect(existsSync(file)).toBe(false)
    else
      await operation.nativeProof(request)
  })
}
