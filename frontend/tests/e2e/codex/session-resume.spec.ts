import { existsSync, readFileSync } from 'node:fs'
import { join } from 'node:path'
import { expect } from '@playwright/test'
import { CODEX_E2E_SKIP_REASON, codexTest } from '../codex-fixtures'
import { runReviewedNativeToolTurn } from '../helpers/nativeAutoReview'
import { expectNoNativeControl } from '../helpers/nativeControlObservation'
import { exerciseSessionResume } from '../helpers/nativeLifecycle'
import { expectNativeOptionValue } from '../helpers/nativeScenario'
import { runNativeToolTurn } from '../helpers/nativeToolExecution'
import { exerciseWorkerRestart } from '../helpers/nativeWorkerRestart'
import { bashToolCall, codexEscalatedCommandToolCall } from '../helpers/providerToolCalls'
import { createTestDirectory } from '../helpers/runDirectory'
import { quotePosixShellArgument } from '../helpers/shellArguments'
import { applyPermissionPreset, expectSettingsOptionChosen } from '../helpers/ui'
import { createProviderProcessTest } from '../provider-fixture-factory'
import { codexAutoReviewRule } from './autoReview'
import { CODEX_AGENT, nativeContext } from './scenarios'

codexTest('reopens the native session and restores its saved UI transcript', async ({ native }) => {
  await exerciseSessionResume(native)
})

const restartTest = createProviderProcessTest({ agent: CODEX_AGENT, nativeContext, skipReason: CODEX_E2E_SKIP_REASON })

for (const restart of ['worker', 'hub-and-worker'] as const) {
  restartTest(`retains native context and transcript after a ${restart} restart`, async ({ native, separateHubWorker }) => {
    await exerciseWorkerRestart(native, separateHubWorker, restart)
  })
}

for (const preset of ['smart', 'bypass'] as const) {
  restartTest(`retains the native ${preset} permission behavior after a Worker restart`, async ({ native, separateHubWorker }) => {
    const ruleName = 'codex-restored-preset-review'
    if (preset === 'smart') {
      await native.modelScript.rule(codexAutoReviewRule(ruleName, {
        outcome: 'allow',
        risk_level: 'low',
        user_authorization: 'high',
        rationale: 'The user requested this isolated fixture write.',
      }))
    }
    await applyPermissionPreset(native.page, preset)
    await exerciseWorkerRestart(native, separateHubWorker, 'worker')
    const permissionMode = preset === 'smart'
      ? 'auto-review'
      : 'never'
    const profile = preset === 'smart'
      ? ':workspace'
      : ':danger-full-access'
    await expectSettingsOptionChosen(native.page, `permissionMode-${permissionMode}`)
    await expectSettingsOptionChosen(native.page, `permission_profile-${profile}`)
    await expectNativeOptionValue(native, 'permissionMode', permissionMode)
    await expectNativeOptionValue(native, 'permission_profile', profile)

    const file = join(createTestDirectory(`codex-restored-${preset}-`), 'result.txt')
    expect(existsSync(file)).toBe(false)
    const marker = `RESTORED_${preset}_42`
    const command = `printf %s ${quotePosixShellArgument(marker)} > ${quotePosixShellArgument(file)}`
    const call = preset === 'smart'
      ? codexEscalatedCommandToolCall('restored-smart-write', command)
      : bashToolCall(native.provider, 'restored-bypass-write', command)
    const turn = {
      toolCalls: [call],
      prompt: 'Run the scripted write outside this workspace after the Worker restart.',
      answer: `The restored ${preset} preset allowed the write.`,
    }
    if (preset === 'smart') {
      await runReviewedNativeToolTurn(native, ruleName, turn)
    }
    else {
      await expectNoNativeControl(native, {
        testId: 'control-banner',
        relatedProof: () => runNativeToolTurn(native, { ...turn, permissions: 'none' }),
      })
    }
    expect(readFileSync(file, 'utf8')).toBe(marker)
  })
}
