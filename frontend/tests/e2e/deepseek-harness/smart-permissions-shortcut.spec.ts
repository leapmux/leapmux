import { existsSync, mkdirSync, writeFileSync } from 'node:fs'
import { join } from 'node:path'
import { expect } from '@playwright/test'
import { DEEPSEEK_HARNESS_OPTION, DEEPSEEK_HARNESS_PERMISSION_PRESET } from '../../../src/generated/contracts/deepseek-harness-protocol'
import { escapeRegExp } from '../../../src/lib/regexp'
import { deepseekHarnessTest } from '../deepseek-harness-fixtures'
import { exerciseSmartPermissions } from '../helpers/nativeBypassPermissions'
import { exerciseNativePermissionDecision } from '../helpers/nativePermission'
import { currentNativeAgent, expectNativeOptionValue, nativeOptionValue } from '../helpers/nativeScenario'
import { nativeToolResult } from '../helpers/nativeToolResult'
import { bashToolCall } from '../helpers/providerToolCalls'
import { quotePosixShellArgument } from '../helpers/shellArguments'
import { applyPermissionPreset, savedControlAnswer, waitForSettingsIdle } from '../helpers/ui'

/** The sentence that opens the system prompt of each Auto review request (`REVIEW_POLICY` of dsh 0.2.1-rc.2). */
const REVIEWER_SYSTEM = 'You are the final authorization reviewer for exactly one pending tool call.'

/** The rule that makes the review allow each call. */
const REVIEW_ALLOWS = 'the Auto review allows the call'

/** The rule that makes the review deny each call. */
const REVIEW_DENIES = 'the Auto review denies the call'

/** The reason that the denying review states. */
const DENIAL_REASON = 'The scripted review does not authorize this removal.'

// Smart selects Auto review. Before each tool call, DeepSeek Harness asks the session's model to review it, and an
// allowed call runs without a sandbox and without a question. The workspace opens in Full access, which reviews
// nothing, so each review proves that the shortcut changed the native preset.
deepseekHarnessTest('executes the native Smart command under Auto review before and after reload', async ({ native }) => {
  await native.modelScript.rule({
    name: REVIEW_ALLOWS,
    priority: 'high',
    when: {
      system: escapeRegExp(REVIEWER_SYSTEM),
    },
    respond: { text: '{"risk":"low","decision":"allow"}' },
  })
  await exerciseSmartPermissions(native, {
    settingsProof: agent => expect(nativeOptionValue(agent, DEEPSEEK_HARNESS_OPTION.Permissions)).toBe(DEEPSEEK_HARNESS_PERMISSION_PRESET.Auto),
  })
  const status = await native.modelScript.status()
  expect(status.ruleMatches[REVIEW_ALLOWS], 'the review decided each removal').toBe(2)
})

// Auto review asks the user about each call that the review denies, and the banner states the review's reason.
deepseekHarnessTest('asks about a call that the Auto review denies, and runs it only when allowed', async ({ native }) => {
  const { page, modelScript } = native
  await modelScript.rule({
    name: REVIEW_DENIES,
    priority: 'high',
    when: {
      system: escapeRegExp(REVIEWER_SYSTEM),
    },
    respond: {
      text: JSON.stringify({ risk: 'medium', decision: 'deny', reason: DENIAL_REASON }),
    },
  })
  await applyPermissionPreset(page, 'smart')
  await waitForSettingsIdle(page)
  await expectNativeOptionValue(native, DEEPSEEK_HARNESS_OPTION.Permissions, DEEPSEEK_HARNESS_PERMISSION_PRESET.Auto)
  const workingDir = (await currentNativeAgent(native)).workingDir
  // The saved rows of the session, in order: each one reads the decision of its banner.
  const saved: string[] = []
  for (const decision of ['deny', 'allow'] as const) {
    const target = join(workingDir, `native-auto-review-${decision}`)
    mkdirSync(target)
    writeFileSync(join(target, 'keep.txt'), 'Only an allowed removal removes this file.\n')
    const id = `native-auto-review-${decision}`
    saved.push(decision === 'allow'
      ? 'Allow once'
      : 'Deny')
    const expectedSaved = [...saved]
    await exerciseNativePermissionDecision(native, {
      toolCall: bashToolCall(native.provider, id, `rm -rf ${quotePosixShellArgument(target)}; printf 'REVIEWED-%s' 42`),
      decision,
      beforeDecision: async (banner) => {
        await expect(banner).toContainText(`Auto review denied this call: ${DENIAL_REASON}`)
        expect(existsSync(join(target, 'keep.txt')), 'the denied call waits for the decision').toBe(true)
      },
      nativeProof: (request) => {
        const result = nativeToolResult(request, id)
        if (decision === 'allow') {
          expect(existsSync(target), 'the allowed call ran').toBe(false)
          expect(result).toContain('REVIEWED-42')
        }
        else {
          expect(existsSync(join(target, 'keep.txt')), 'the denied call never ran').toBe(true)
          expect(result).toMatch(/rejected/i)
        }
      },
      viewProof: () => expect(savedControlAnswer(page)).toHaveText(expectedSaved),
    })
  }
  const status = await modelScript.status()
  expect(status.ruleMatches[REVIEW_DENIES], 'the review decided each call').toBe(2)
})

// Track the native route and its LeapMux integration: https://github.com/leapmux/leapmux/issues/514.
