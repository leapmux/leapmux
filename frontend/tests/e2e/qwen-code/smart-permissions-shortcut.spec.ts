import { existsSync } from 'node:fs'
import { join } from 'node:path'
import { expect } from '@playwright/test'
import { exerciseNativePermissionRefusal } from '../helpers/nativePermission'
import { currentNativeAgent } from '../helpers/nativeScenario'
import { writeToolCall } from '../helpers/providerToolCalls'
import { applyPermissionPreset, waitForSettingsHydrated } from '../helpers/ui'
import { qwenTest } from '../qwen-fixtures'
import { expectQwenCanceledTool, qwenClassifierWithoutVerdict } from './autoClassifier'

const CLASSIFIER_RULE = 'qwen-auto-classifier-states-no-verdict'

// Smart is Qwen's Auto mode. Before Auto mode runs a write to a protected file, it asks a classifier model.
// `qwenClassifierWithoutVerdict` states why a rule answers that request and what an answer with no verdict does.
qwenTest('requires real native review for protected writes under Smart before and after reload', async ({ native }) => {
  const { page, modelScript } = native
  const agent = await currentNativeAgent(native)
  const file = join(agent.workingDir, 'package.json')
  await applyPermissionPreset(page, 'smart')
  await modelScript.rule(qwenClassifierWithoutVerdict(CLASSIFIER_RULE))
  for (const reload of [false, true]) {
    if (reload) {
      await page.reload()
      await waitForSettingsHydrated(page)
    }
    const id = `native-smart-write-${reload}`
    const toolCall = writeToolCall(native.provider, id, { path: file, content: '{"private":true}\n' })
    const classifiedBefore = (await modelScript.status()).ruleMatches[CLASSIFIER_RULE] ?? 0
    await exerciseNativePermissionRefusal(native, {
      toolCall,
      prompt: 'Run the scripted permission probe.',
      bannerText: 'package.json',
      expectUnchanged: () => expect(existsSync(file)).toBe(false),
      nativeRefusal: snapshot => expectQwenCanceledTool(snapshot, toolCall),
    })
    expect((await modelScript.status()).ruleMatches[CLASSIFIER_RULE], 'Auto mode asked its classifier before it asked the reader').toBeGreaterThan(classifiedBefore)
  }
})
