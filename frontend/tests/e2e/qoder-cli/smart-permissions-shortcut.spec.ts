import { existsSync, readFileSync } from 'node:fs'
import { join } from 'node:path'
import { expect } from '@playwright/test'
import { QODER_MODE } from '../../../src/generated/contracts/qoder-protocol'
import { isObject } from '../../../src/lib/jsonPick'
import { runReviewedNativeToolTurn } from '../helpers/nativeAutoReview'
import { watchNativeControls } from '../helpers/nativeControlWatch'
import { exerciseNativePermissionDecision } from '../helpers/nativePermission'
import { currentNativeAgent } from '../helpers/nativeScenario'
import { nativeToolResult } from '../helpers/nativeToolResult'
import { bashToolCall } from '../helpers/providerToolCalls'
import { quotePosixShellArgument } from '../helpers/shellArguments'
import { applyPermissionPreset, chooseSettingsOption, expectNoControlBanner, expectPermissionShortcuts, expectSettingsOptionChosen, waitForSettingsHydrated, waitForSettingsIdle } from '../helpers/ui'
import { qoderTest } from '../qoder-fixtures'
import { qoderAutoReviewRule } from './autoReview'
import { expectQoderModeChip } from './scenarios'

qoderTest.describe('Qoder CLI settings', () => {
  // The catalog check stays separate from the native fallback proof below.
  qoderTest('the Smart shortcut selects Auto mode', async ({ authenticatedQoderWorkspace, page }) => {
    void authenticatedQoderWorkspace
    await waitForSettingsHydrated(page, 'permissionMode')
    await expectPermissionShortcuts(page, { smart: 'offered' })

    await applyPermissionPreset(page, 'smart')
    await expectQoderModeChip(page, 'Auto')
    await expectSettingsOptionChosen(page, `permissionMode-${QODER_MODE.Auto}`)
  })
})

// A failed native reviewer must leave protected calls for manual approval.
for (const decision of ['allow', 'deny'] as const) {
  qoderTest(`uses manual ${decision} in Auto mode before and after reload`, async ({ native }) => {
    const ruleName = 'qoder-auto-review'
    await native.modelScript.rule({
      ...qoderAutoReviewRule(ruleName, false, 'The user requested this isolated fixture write.'),
      respond: {
        error: { status: 400, message: 'The scripted reviewer is unavailable.' },
      },
    })
    const agent = await currentNativeAgent(native)
    const controls = await watchNativeControls(native.leapmuxServer, agent.id)
    try {
      await chooseSettingsOption(native.page, `permissionMode-${QODER_MODE.Auto}`)
      await waitForSettingsIdle(native.page)
      for (const reload of [false, true]) {
        if (reload) {
          await native.page.reload()
          await waitForSettingsHydrated(native.page, 'permissionMode')
        }
        await expectSettingsOptionChosen(native.page, `permissionMode-${QODER_MODE.Auto}`)
        const reviewsBefore = (await native.modelScript.status()).ruleMatches[ruleName] ?? 0
        const fileName = `auto-${decision}-${Number(reload)}.txt`
        const file = join(agent.workingDir, fileName)
        expect(existsSync(file)).toBe(false)
        const content = native.modelScript.prompt(`QODER_AUTO_${decision}_${Number(reload)}`)
        const call = bashToolCall(native.provider, `qoder-auto-${decision}-${Number(reload)}`, `printf %s ${quotePosixShellArgument(content)} > ${quotePosixShellArgument(file)}`)
        await exerciseNativePermissionDecision(native, {
          toolCall: call,
          decision,
          beforeDecision: async (banner) => {
            await expect(banner).toContainText(fileName)
            expect(existsSync(file)).toBe(false)
            await expect.poll(() => controls.controls().some(control =>
              isObject(control.payload.request) && control.payload.request.tool_use_id === call.id,
            )).toBe(true)
            expect(controls.controls()).toEqual(expect.arrayContaining([
              expect.objectContaining({
                payload: expect.objectContaining({
                  type: 'control_request',
                  request: expect.objectContaining({ subtype: 'can_use_tool', tool_name: 'Bash', tool_use_id: call.id }),
                }),
              }),
            ]))
            expect((await native.modelScript.status()).ruleMatches[ruleName] ?? 0).toBeGreaterThan(reviewsBefore)
          },
          nativeProof: async (request) => {
            const result = nativeToolResult(request, call.id)
            if (decision === 'allow') {
              expect(result).not.toMatch(/denied|rejected|not allowed/i)
              expect(readFileSync(file, 'utf8')).toBe(content)
            }
            else {
              expect(result).toMatch(/denied|rejected|not allowed/i)
              expect(existsSync(file)).toBe(false)
            }
            expect((await native.modelScript.status()).ruleMatches[ruleName] ?? 0).toBeGreaterThan(reviewsBefore)
          },
          viewProof: () => expectNoControlBanner(native.page),
        })
      }
    }
    finally {
      controls.cancel()
    }
  })
}

qoderTest('uses the native Smart reviewer before and after reload', async ({ native }) => {
  const ruleName = 'qoder-smart-review'
  await native.modelScript.rule(qoderAutoReviewRule(ruleName, false, 'The user requested this isolated fixture write.'))
  const agent = await currentNativeAgent(native)
  await applyPermissionPreset(native.page, 'smart')
  for (const reload of [false, true]) {
    if (reload) {
      await native.page.reload()
      await waitForSettingsHydrated(native.page, 'permissionMode')
    }
    await expectSettingsOptionChosen(native.page, `permissionMode-${QODER_MODE.Auto}`)
    const file = join(agent.workingDir, `smart-review-${Number(reload)}.txt`)
    expect(existsSync(file)).toBe(false)
    const content = native.modelScript.prompt(`QODER_SMART_${Number(reload)}_42`)
    const call = bashToolCall(native.provider, `qoder-smart-${Number(reload)}`, `printf %s ${quotePosixShellArgument(content)} > ${quotePosixShellArgument(file)}`)
    const { resultRequest } = await runReviewedNativeToolTurn(native, ruleName, {
      toolCalls: [call],
      prompt: 'Run the scripted write under Smart permissions.',
      answer: `The reviewer allowed write ${Number(reload)}.`,
    })
    expect(nativeToolResult(resultRequest, call.id)).not.toMatch(/denied|rejected|not allowed/i)
    expect(readFileSync(file, 'utf8')).toBe(content)
  }
})
