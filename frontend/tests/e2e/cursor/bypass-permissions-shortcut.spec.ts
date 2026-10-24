import type { ManagedNativeScenarioContext } from '../helpers/nativeScenario'
import { expect } from '@playwright/test'
import { CURSOR_OPTION } from '../../../src/generated/contracts/cursor-protocol'
import { AgentOptionSettlementState, UpdateAgentSettingsRequestSchema, UpdateAgentSettingsResponseSchema } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { CURSOR_E2E_SKIP_REASON, cursorTest } from '../cursor-fixtures'
import { getTestChannel } from '../helpers/api'
import { exerciseBypassPermissions } from '../helpers/nativeBypassPermissions'
import { expectNoNativeControl } from '../helpers/nativeControlObservation'
import { currentNativeAgent, nativeOptionValue } from '../helpers/nativeScenario'
import { exerciseWorkerRestart } from '../helpers/nativeWorkerRestart'
import { cursorWebFetchPermissionToolCall } from '../helpers/providerToolCalls'
import { answerControl, assistantBubbles, closeComposerMenus, expectPermissionShortcuts, offeredSettingsOptions, openSettingsMenu, sendMessage, tabById, waitForAgentIdle, waitForControlBanner, waitForNativeSettingsHydrated } from '../helpers/ui'
import { openProviderAgent } from '../helpers/workspace'
import { createProviderProcessTest } from '../provider-fixture-factory'
import { cursorNativeToolOutput } from './nativeExecutionScenario'
import { CURSOR_AGENT, nativeContext } from './scenarios'

cursorTest('keeps native Bypass enabled and requires a new session to restore permission prompts', async ({ native }) => {
  const { page, modelScript } = native
  await waitForNativeSettingsHydrated(page)
  await expectPermissionShortcuts(page, { smart: 'absent', bypass: 'offered' })
  const before = await currentNativeAgent(native)
  await exerciseBypassPermissions({
    ...native,
    readToolResult: async (_request, callId) => {
      const output = await cursorNativeToolOutput(native, callId)
      expect(output.exitCode).toBe(0)
      if (typeof output.stdout !== 'string' || typeof output.stderr !== 'string')
        throw new Error('The native Cursor command has no stdout or stderr.')
      return { text: output.stdout + output.stderr, exitCode: 0 }
    },
  }, {
    answerStep: 'same-step',
    settingsProof: (agent) => {
      expect(nativeOptionValue(agent, CURSOR_OPTION.BypassPermissions)).toBe('true')
      expect(nativeOptionValue(agent, 'model')).toBe(nativeOptionValue(before, 'model'))
      expect(nativeOptionValue(agent, 'permissionMode')).toBe(nativeOptionValue(before, 'permissionMode'))
    },
  })

  await expectBypassLocked(native)
  const enabled = await currentNativeAgent(native)
  const server = native.leapmuxServer
  const channel = await getTestChannel(server.hubUrl, server.adminToken)
  const response = await channel.callWorker(server.workerId, 'UpdateAgentSettings', UpdateAgentSettingsRequestSchema, UpdateAgentSettingsResponseSchema, {
    agentId: enabled.id,
    settings: { options: { [CURSOR_OPTION.BypassPermissions]: 'false', model: 'mock-grok' } },
  })
  expect(response.optionSettlements[CURSOR_OPTION.BypassPermissions]?.state).toBe(AgentOptionSettlementState.CONFIRMED)
  expect(response.optionSettlements[CURSOR_OPTION.BypassPermissions]?.value).toBe('true')
  const refused = await currentNativeAgent(native)
  expect(refused.agentSessionId).toBe(enabled.agentSessionId)
  expect(nativeOptionValue(refused, 'model')).toBe(nativeOptionValue(enabled, 'model'))
  await expectBypassLocked(native)
  await expectNativeWebFetchBypassesPermission(native, 'cursor-refused-off')

  const fresh = await openProviderAgent(server, native.workspaceId, CURSOR_AGENT, { optionValues: { [CURSOR_OPTION.BypassPermissions]: 'false' } })
  await tabById(page, fresh.agentId).click()
  await waitForNativeSettingsHydrated(page)
  const off = await currentNativeAgent(native)
  expect(off.agentSessionId).not.toBe(enabled.agentSessionId)
  expect(nativeOptionValue(off, CURSOR_OPTION.BypassPermissions)).toBe('false')
  expect(await offeredSettingsOptions(page, CURSOR_OPTION.BypassPermissions)).toEqual(['false', 'true'])
  const url = 'https://cursor-restored-permission.invalid/probe'
  const start = await modelScript.queue({ toolCalls: [cursorWebFetchPermissionToolCall('cursor-restored-permission', url)] })
  await sendMessage(page, modelScript.prompt('Ask permission to fetch the scripted URL in the new session with Bypass Off.'))
  await modelScript.waitForSteps(start + 1)
  await expect(await waitForControlBanner(page)).toContainText(url)
  await answerControl(page, 'deny')
  await waitForAgentIdle(page)
  await expect(assistantBubbles(page).filter({ hasText: 'Cursor web fetch rejected: User rejected' })).toBeVisible()
})

async function expectBypassLocked(context: ManagedNativeScenarioContext): Promise<void> {
  expect(nativeOptionValue(await currentNativeAgent(context), CURSOR_OPTION.BypassPermissions)).toBe('true')
  expect(await offeredSettingsOptions(context.page, CURSOR_OPTION.BypassPermissions)).toEqual(['true'])
  const menu = await openSettingsMenu(context.page, CURSOR_OPTION.BypassPermissions)
  const option = menu.getByTestId(`${CURSOR_OPTION.BypassPermissions}-true`)
  await expect(option).toBeDisabled()
  await option.hover()
  await expect(context.page.getByRole('tooltip').filter({ visible: true })).toContainText('Start a new session to turn it off')
  await closeComposerMenus(context.page)
}

const restartTest = createProviderProcessTest({
  agent: CURSOR_AGENT,
  nativeContext,
  skipReason: CURSOR_E2E_SKIP_REASON,
  openOptions: { optionValues: { [CURSOR_OPTION.BypassPermissions]: 'true' } },
})

restartTest('retains read-only native Bypass after a worker restart', async ({ native, separateHubWorker }) => {
  await exerciseWorkerRestart(native, separateHubWorker, 'worker')
  await expectBypassLocked(native)
  await expectNativeWebFetchBypassesPermission(native, 'cursor-restarted-bypass')
})

async function expectNativeWebFetchBypassesPermission(context: ManagedNativeScenarioContext, callId: string): Promise<void> {
  await expectNoNativeControl(context, { testId: 'control-banner', relatedProof: async () => {
    const start = await context.modelScript.queue({ toolCalls: [cursorWebFetchPermissionToolCall(callId, `https://${callId}.invalid/probe`)] })
    await sendMessage(context.page, context.modelScript.prompt('Fetch the scripted URL with native Bypass enabled.'))
    await context.modelScript.waitForSteps(start + 1)
    await waitForAgentIdle(context.page)
    await expect(assistantBubbles(context.page).filter({ hasText: 'Cursor web fetch approved' })).toBeVisible()
  } })
}
