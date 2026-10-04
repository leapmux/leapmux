import type { Page } from '@playwright/test'
import type { ModelScript } from '../helpers/modelScriptFixture'
import { expect } from '@playwright/test'
import { withCleanup } from '../helpers/cleanup'
import { queuedInputRow, steerButton } from '../helpers/steer'
import { sendMessage, waitForAgentIdle } from '../helpers/ui'
import { ZCODE_E2E_SKIP_REASON, zcodeTest } from '../zcode-fixtures'

zcodeTest.skip(!!ZCODE_E2E_SKIP_REASON, ZCODE_E2E_SKIP_REASON || '')

async function proveNoNativeSteer(page: Page, modelScript: ModelScript, gate: string): Promise<void> {
  const queuedText = 'Deliver this after the first turn ends.'
  await withCleanup(async () => {
    await modelScript.queue(
      { text: 'The first turn ended.', gate },
      { text: 'The queued message arrived.' },
    )
    await sendMessage(page, modelScript.prompt('Wait for my next message.'))
    await modelScript.waitForGate(gate)

    await sendMessage(page, queuedText)
    const queued = queuedInputRow(page, queuedText)
    await expect(queued).toBeVisible()
    await expect(steerButton(queued)).toHaveCount(0)
    await modelScript.releaseGate(gate)
    const status = await modelScript.waitForSteps()
    const next = status.requests.find(request => request.stepIndex === 1)
    expect(JSON.stringify(next?.body)).toContain(queuedText)
    await waitForAgentIdle(page)
  }, async () => {
    await modelScript.releaseGateIfHeld(gate)
  })
}

zcodeTest('offers no Steer control while the native prompt holds the session', async ({ authenticatedZCodeWorkspace, page, modelScript }) => {
  void authenticatedZCodeWorkspace
  await proveNoNativeSteer(page, modelScript, 'zcode-no-steer')
})
