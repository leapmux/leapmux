import { existsSync, mkdirSync, writeFileSync } from 'node:fs'
import { join } from 'node:path'
import { expect } from '@playwright/test'
import { CODEBUDDY_MODE } from '../../../src/generated/contracts/codebuddy-protocol'
import { escapeRegExp } from '../../../src/lib/regexp'
import { codebuddyTest } from '../codebuddy-fixtures'
import { CODEBUDDY_CLASSIFIER_SYSTEM_OPENING } from '../helpers/codebuddySurface'
import { exerciseSmartPermissions } from '../helpers/nativeBypassPermissions'
import { expectNoNativeControl } from '../helpers/nativeControlObservation'
import { currentNativeAgent, expectNativeOptionValue, nativeOptionValue, nativeTextStep, nativeToolOutcome, toolTurnSteps } from '../helpers/nativeScenario'
import { bashToolCall } from '../helpers/providerToolCalls'
import { quotePosixShellArgument } from '../helpers/shellArguments'
import { applyPermissionPreset, assistantBubbles, chooseSettingsOption, expectSettingsChip, sendMessage, waitForAgentIdle, waitForSettingsHydrated, waitForSettingsIdle } from '../helpers/ui'

/** The rule that answers the classifier. */
const CLASSIFIER_RULE = 'the Auto classifier allows the call'

/** The rule that makes the classifier block each call. */
const CLASSIFIER_BLOCK_RULE = 'the Auto classifier blocks the call'

/** The reason that the blocking classifier states. */
const BLOCK_REASON = 'The scripted classifier blocks this removal.'

// Smart selects CodeBuddy's Auto mode. Before each call that would prompt, CodeBuddy asks a classifier on the
// session's model, and `<block>no</block>` allows the call, so the removal runs without a question.
codebuddyTest('executes the native Smart command in Auto mode before and after reload', async ({ native }) => {
  const { page, modelScript } = native
  await modelScript.rule({
    name: CLASSIFIER_RULE,
    priority: 'high',
    when: {
      system: escapeRegExp(CODEBUDDY_CLASSIFIER_SYSTEM_OPENING),
    },
    respond: { text: '<block>no</block>' },
  })
  await exerciseSmartPermissions(native, {
    // The workspace opens in Bypass. Default asks before a tool, so the shortcut must change the native behavior.
    prepare: async () => {
      await waitForSettingsHydrated(page, 'permissionMode')
      await chooseSettingsOption(page, `permissionMode-${CODEBUDDY_MODE.Default}`)
      await waitForSettingsIdle(page)
      await expectNativeOptionValue(native, 'permissionMode', CODEBUDDY_MODE.Default)
    },
    settingsProof: agent => expect(nativeOptionValue(agent, 'permissionMode')).toBe(CODEBUDDY_MODE.Auto),
  })
  await expectSettingsChip(page, 'Auto')
  const status = await modelScript.status()
  expect(status.ruleMatches[CLASSIFIER_RULE], 'the classifier decided each removal').toBeGreaterThanOrEqual(2)
})

// A blocked call fails with CodeBuddy's own denial, and Auto asks no question in its place.
codebuddyTest('runs no call that the Auto classifier blocks, and asks no question', async ({ native }) => {
  const { page, modelScript } = native
  await modelScript.rule({
    name: CLASSIFIER_BLOCK_RULE,
    priority: 'high',
    when: {
      system: escapeRegExp(CODEBUDDY_CLASSIFIER_SYSTEM_OPENING),
    },
    respond: { text: `<block>yes</block><reason>${BLOCK_REASON}</reason>` },
  })
  await applyPermissionPreset(page, 'smart')
  await waitForSettingsIdle(page)
  await expectNativeOptionValue(native, 'permissionMode', CODEBUDDY_MODE.Auto)
  const target = join((await currentNativeAgent(native)).workingDir, 'native-smart-blocked')
  mkdirSync(target)
  writeFileSync(join(target, 'keep.txt'), 'Only a classifier that allows the removal removes this file.\n')
  const id = 'native-smart-blocked'
  const answer = 'The classifier blocked the removal.'
  await expectNoNativeControl(native, {
    testId: 'control-banner',
    relatedProof: async () => {
      const steps = toolTurnSteps([
        bashToolCall(native.provider, id, `rm -rf ${quotePosixShellArgument(target)}`),
      ], nativeTextStep(native, answer))
      const start = await modelScript.queue(...steps)
      await sendMessage(page, modelScript.prompt('Run the scripted removal that the classifier blocks.'))
      await modelScript.waitForSteps(start + steps.length)
      await waitForAgentIdle(page)
      expect(existsSync(join(target, 'keep.txt')), 'the blocked command never ran').toBe(true)
      const outcome = await nativeToolOutcome(native, await modelScript.requestAt(start + steps.length - 1), id)
      expect(outcome.text).toContain('denied by the CodeBuddy auto mode classifier')
      expect(outcome.text).toContain(BLOCK_REASON)
      await expect(assistantBubbles(page).filter({ hasText: answer }).first()).toBeVisible()
    },
  })
  const status = await modelScript.status()
  // Stage 1 blocks, so CodeBuddy asks stage 2, which blocks too.
  expect(status.ruleMatches[CLASSIFIER_BLOCK_RULE], 'both classifier stages decided the call').toBe(2)
})

// Track the native route and its LeapMux integration: https://github.com/leapmux/leapmux/issues/514.
