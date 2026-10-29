import type { ManagedNativeScenarioContext } from '../helpers/nativeScenario'
import { existsSync, readFileSync } from 'node:fs'
import { join } from 'node:path'
import { expect } from '@playwright/test'
import { agentOpenOptions } from '../agentSettings'
import { openAgentViaAPI } from '../helpers/api'
import { expectNoNativeControl } from '../helpers/nativeControlObservation'
import { exerciseNativePermissionDecision } from '../helpers/nativePermission'
import { currentNativeAgent, nativeOptionValue, nativeToolOutcome } from '../helpers/nativeScenario'
import { runNativeToolTurn } from '../helpers/nativeToolExecution'
import { withPrivateNativeWorkspace } from '../helpers/privateNativeWorkspace'
import { bashToolCall } from '../helpers/providerToolCalls'
import { applyPermissionPreset, chooseSettingsOption, expectPermissionShortcuts, waitForNativeSettingsHydrated, waitForSettingsIdle } from '../helpers/ui'
import { reasonixTest } from '../reasonix-fixtures'
import { prepareReasonixPermissionRules, REASONIX_SMART_ASK_COMMAND, REASONIX_SMART_DENY_COMMAND } from './permissionScenario'
import { nativeContext, REASONIX_AGENT } from './scenarios'
import { exerciseReasonixSessionSettings } from './settingsScenario'

// Reasonix 1.38 asks to leave Plan mode after each Plan-mode answer, also under the Bypass preset. The settings scenario
// answers each of those requests, so no request stays open across the Bypass change and the reload.
reasonixTest('smart-permissions-shortcut: applies Reasonix session settings and preserves them after reload', async ({ native }) => {
  await exerciseReasonixSessionSettings(native)
})

reasonixTest('selects native Auto while retaining explicit ask and deny rules after reload', async ({ page, leapmuxServer, modelScript }) => {
  await withPrivateNativeWorkspace(page, leapmuxServer, {
    prefix: 'reasonix-smart',
    workerName: 'Reasonix Smart permission test',
    providerAgent: REASONIX_AGENT,
    prepare: runDirectory => prepareReasonixPermissionRules(runDirectory, leapmuxServer.mockModelUrl),
    openAgent: (server, workspaceId, workingDir) => openAgentViaAPI(server, workspaceId, workingDir, agentOpenOptions(REASONIX_AGENT.provider)),
  }, async ({ server, workspaceId, workingDir }) => {
    const native = await nativeContext({
      page,
      leapmuxServer: server,
      workspaceId,
      modelScript,
    })
    await waitForNativeSettingsHydrated(page)
    const before = await currentNativeAgent(native)
    await expectPermissionShortcuts(page, { smart: 'offered', bypass: 'offered' })
    await applyPermissionPreset(page, 'smart')
    await waitForSettingsIdle(page)
    for (const pass of [0, 1]) {
      if (pass === 1) {
        await page.reload()
        await waitForNativeSettingsHydrated(page)
      }
      const current = await currentNativeAgent(native)
      expect(current.agentSessionId).toBe(before.agentSessionId)
      expect(nativeOptionValue(current, 'tool_approval')).toBe('auto')
      for (const id of ['model', 'permissionMode', 'effort'])
        expect(nativeOptionValue(current, id)).toBe(nativeOptionValue(before, id))

      const allowedFile = join(workingDir, `reasonix-smart-allowed-${pass}.txt`)
      expect(existsSync(allowedFile)).toBe(false)
      await runWithoutPermission(native, `reasonix-smart-allow-${pass}`, `printf 'SMART%s' 42 > reasonix-smart-allowed-${pass}.txt`)
      expect(readFileSync(allowedFile, 'utf8')).toBe('SMART42')

      const denied = await runWithoutPermission(native, `reasonix-smart-deny-${pass}`, REASONIX_SMART_DENY_COMMAND)
      expect(denied).toContain(`blocked: Matched permission rule: deny Bash=${REASONIX_SMART_DENY_COMMAND}`)
      expect(denied).toContain('denied by permission policy')
      expect(existsSync(join(workingDir, 'reasonix-smart-denied.txt'))).toBe(false)
      await exerciseNativePermissionDecision(native, {
        toolCall: bashToolCall(native.provider, `reasonix-smart-ask-${pass}`, REASONIX_SMART_ASK_COMMAND),
        decision: 'deny',
        beforeDecision: banner => expect(banner).toContainText(REASONIX_SMART_ASK_COMMAND),
        nativeProof: () => expect(existsSync(join(workingDir, 'reasonix-smart-ask.txt'))).toBe(false),
      })
    }

    const nested = 'sh -c "printf nested > reasonix-smart-nested.txt"'
    await exerciseNativePermissionDecision(native, {
      toolCall: bashToolCall(native.provider, 'reasonix-smart-nested', nested),
      decision: 'deny',
      beforeDecision: banner => expect(banner).toContainText(nested),
      nativeProof: () => expect(existsSync(join(workingDir, 'reasonix-smart-nested.txt'))).toBe(false),
    })
    await chooseSettingsOption(page, 'tool_approval-ask')
    await waitForSettingsIdle(page)
    expect(nativeOptionValue(await currentNativeAgent(native), 'tool_approval')).toBe('ask')
    const ordinary = 'printf ask-mode > reasonix-smart-restored.txt'
    await exerciseNativePermissionDecision(native, {
      toolCall: bashToolCall(native.provider, 'reasonix-smart-restored', ordinary),
      decision: 'deny',
      beforeDecision: banner => expect(banner).toContainText(ordinary),
      nativeProof: () => expect(existsSync(join(workingDir, 'reasonix-smart-restored.txt'))).toBe(false),
    })
  })
})

async function runWithoutPermission(context: ManagedNativeScenarioContext, callId: string, command: string): Promise<string> {
  let output = ''
  await expectNoNativeControl(context, {
    testId: 'control-banner',
    relatedProof: async () => {
      const { resultRequest } = await runNativeToolTurn(context, {
        toolCalls: [
          bashToolCall(context.provider, callId, command),
        ],
        prompt: 'Run the scripted command under native Auto approval.',
        answer: `The native Auto command ended: ${callId}.`,
        permissions: 'none',
      })
      output = (await nativeToolOutcome(context, resultRequest, callId)).text
    },
  })
  return output
}
