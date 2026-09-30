import type { Page } from '@playwright/test'
import type { ModelScript } from './helpers/modelScriptFixture'
import { AgentProvider } from '../../src/generated/proto/leapmux/v1/agent_pb'
import { COPILOT_E2E_SKIP_REASON, copilotTest } from './copilot-fixtures'
import { expect } from './fixtures'
import { GOOSE_E2E_SKIP_REASON, gooseTest } from './goose-fixtures'
import { exerciseProviderSteer } from './helpers/providerSteer'
import { queuedInputRow, steerButton } from './helpers/steer'
import { sendMessage, waitForAgentIdle } from './helpers/ui'
import { KILO_E2E_SKIP_REASON, kiloTest } from './kilo-fixtures'
import { OPENCODE_E2E_SKIP_REASON, opencodeTest } from './opencode-fixtures'
import { PI_E2E_SKIP_REASON, piTest } from './pi-fixtures'
import { REASONIX_E2E_SKIP_REASON, reasonixTest } from './reasonix-fixtures'
import { ZCODE_E2E_SKIP_REASON, zcodeTest } from './zcode-fixtures'

async function allowShellPermission(page: Page): Promise<void> {
  const banner = page.getByTestId('control-banner').filter({ visible: true })
  await expect(banner).toContainText('printf provider-steer-ready')
  await page.getByTestId('control-actions').getByRole('button', { name: 'Allow', exact: true }).click()
}

async function proveNoNativeSteer(page: Page, modelScript: ModelScript, gate: string): Promise<void> {
  const queuedText = 'Deliver this after the first turn ends.'
  await modelScript.queue(
    { text: 'The first turn ended.', gate },
    { text: 'The queued message arrived.' },
  )
  await sendMessage(page, modelScript.prompt('Wait for my next message.'))
  await modelScript.waitForGate(gate)
  try {
    await sendMessage(page, queuedText)
    const queued = queuedInputRow(page, queuedText)
    await expect(queued).toBeVisible()
    await expect(steerButton(queued)).toHaveCount(0)
  }
  finally {
    await modelScript.releaseGate(gate)
  }
  const status = await modelScript.waitForSteps()
  const next = status.requests.find(request => request.stepIndex === 1)
  expect(JSON.stringify(next?.body)).toContain(queuedText)
  await waitForAgentIdle(page)
}

copilotTest.describe('Copilot mid-turn steering', () => {
  copilotTest.skip(!!COPILOT_E2E_SKIP_REASON, COPILOT_E2E_SKIP_REASON || '')
  copilotTest('places queued guidance in the next native model request', async ({ authenticatedCopilotWorkspace, page, modelScript }) => {
    void authenticatedCopilotWorkspace
    await exerciseProviderSteer(page, modelScript, AgentProvider.GITHUB_COPILOT, { approveTool: allowShellPermission })
  })
})

kiloTest.describe('Kilo mid-turn steering', () => {
  kiloTest.skip(!!KILO_E2E_SKIP_REASON, KILO_E2E_SKIP_REASON || '')
  kiloTest('places queued guidance in the next native model request', async ({ authenticatedKiloWorkspace, page, modelScript }) => {
    void authenticatedKiloWorkspace
    await exerciseProviderSteer(page, modelScript, AgentProvider.KILO, { approveTool: allowShellPermission, resultDividers: 2 })
  })
})

opencodeTest.describe('OpenCode mid-turn steering', () => {
  opencodeTest.skip(!!OPENCODE_E2E_SKIP_REASON, OPENCODE_E2E_SKIP_REASON || '')
  opencodeTest('places queued guidance in the next native model request', async ({ authenticatedOpencodeWorkspace, page, modelScript }) => {
    void authenticatedOpencodeWorkspace
    await exerciseProviderSteer(page, modelScript, AgentProvider.OPENCODE)
  })
})

gooseTest.describe('Goose native steering capability', () => {
  gooseTest.skip(!!GOOSE_E2E_SKIP_REASON, GOOSE_E2E_SKIP_REASON || '')
  gooseTest('offers no Steer control when the native handshake omits it', async ({ authenticatedGooseWorkspace, page, modelScript }) => {
    void authenticatedGooseWorkspace
    await proveNoNativeSteer(page, modelScript, 'goose-no-steer')
  })
})

piTest.describe('Pi mid-turn steering', () => {
  piTest.skip(!!PI_E2E_SKIP_REASON, PI_E2E_SKIP_REASON || '')
  piTest('places queued guidance in the next native model request', async ({ authenticatedPiWorkspace, page, modelScript }) => {
    void authenticatedPiWorkspace
    await exerciseProviderSteer(page, modelScript, AgentProvider.PI)
  })
})

reasonixTest.describe('Reasonix mid-turn steering', () => {
  reasonixTest.skip(!!REASONIX_E2E_SKIP_REASON, REASONIX_E2E_SKIP_REASON || '')
  reasonixTest('places queued guidance in the next native model request', async ({ authenticatedReasonixWorkspace, page, modelScript }) => {
    void authenticatedReasonixWorkspace
    await exerciseProviderSteer(page, modelScript, AgentProvider.REASONIX)
  })
})

zcodeTest.describe('ZCode native steering capability', () => {
  zcodeTest.skip(!!ZCODE_E2E_SKIP_REASON, ZCODE_E2E_SKIP_REASON || '')
  zcodeTest('offers no Steer control while the native prompt holds the session', async ({ authenticatedZCodeWorkspace, page, modelScript }) => {
    void authenticatedZCodeWorkspace
    await proveNoNativeSteer(page, modelScript, 'zcode-no-steer')
  })
})
