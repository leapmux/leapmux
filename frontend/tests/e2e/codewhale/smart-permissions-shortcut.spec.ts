import type { ManagedNativeScenarioContext } from '../helpers/nativeScenario'
import { existsSync, readFileSync } from 'node:fs'
import { join } from 'node:path'
import { expect } from '@playwright/test'
import { codewhaleTest } from '../codewhale-fixtures'
import { runReviewedNativeToolTurn } from '../helpers/nativeAutoReview'
import { currentNativeAgent } from '../helpers/nativeScenario'
import { nativeToolResult } from '../helpers/nativeToolResult'
import { writeToolCall } from '../helpers/providerToolCalls'
import { applyPermissionPreset, waitForSettingsHydrated } from '../helpers/ui'
import { codewhaleAutoReviewRule } from './autoReview'

const SMART_REVIEW_RULE = 'native-codewhale-smart-review'

codewhaleTest('uses the native Smart reviewer and restores its setting after reload', async ({ native }) => {
  await exerciseSmartReview(native, 'allow')
})

codewhaleTest('keeps a denied native Smart write from running before and after reload', async ({ native }) => {
  await exerciseSmartReview(native, 'deny')
})

async function exerciseSmartReview(native: ManagedNativeScenarioContext, decision: 'allow' | 'deny'): Promise<void> {
  const { page, modelScript } = native
  const agent = await currentNativeAgent(native)
  const file = join(agent.workingDir, '.env')
  expect(existsSync(file)).toBe(false)
  await modelScript.rule(codewhaleAutoReviewRule(SMART_REVIEW_RULE, decision, `The scripted reviewer must ${decision} this isolated write.`))
  await applyPermissionPreset(page, 'smart')
  for (const reload of [false, true]) {
    if (reload) {
      await page.reload()
      await waitForSettingsHydrated(page)
    }
    // The guardian request holds only the proposed call and Codewhale's own
    // observations. It never holds the conversation, so it never holds the
    // marked prompt (`build_reviewer_context` in Codewhale's
    // `tui/auto_review.rs`). The written content is the one part of that
    // request that this test controls, so the content carries the scenario
    // marker. Without the marker, the guardian request reaches the ambient
    // scenario, which refuses it, and Codewhale denies the write (fail closed).
    const content = `${modelScript.prompt(reload ? 'NATIVE_SMART_RESTORED=42' : 'NATIVE_SMART_APPLIED=42')}\n`
    // One agent session runs both passes, so each pass gives its tool call its own ID.
    const id = `native-smart-review-${Number(reload)}`
    const { resultRequest } = await runReviewedNativeToolTurn(native, SMART_REVIEW_RULE, {
      toolCalls: [writeToolCall(native.provider, id, { path: file, content })],
      prompt: 'Run the scripted protected fixture write under Smart permissions.',
      answer: `The native reviewer returned ${decision} for write ${Number(reload)}.`,
    })
    // Check the native result first. A denied write states its reason there.
    const result = nativeToolResult(resultRequest, id)
    if (decision === 'allow') {
      expect(result).not.toMatch(/denied|not allowed/i)
      expect(readFileSync(file, 'utf8')).toBe(content)
    }
    else {
      expect(result).toMatch(/denied|not allowed|blocked/i)
      expect(existsSync(file)).toBe(false)
    }
  }
}
