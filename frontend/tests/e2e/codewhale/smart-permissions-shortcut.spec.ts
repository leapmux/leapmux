import { existsSync, readFileSync } from 'node:fs'
import { join } from 'node:path'
import { expect } from '@playwright/test'
import { codewhaleTest } from '../codewhale-fixtures'
import { expectNoNativeControl } from '../helpers/nativeControlObservation'
import { currentNativeAgent, nativeTextStep } from '../helpers/nativeScenario'
import { nativeToolResult } from '../helpers/nativeToolResult'
import { writeToolCall } from '../helpers/providerToolCalls'
import { applyPermissionPreset, sendMessage, waitForAgentIdle, waitForSettingsHydrated } from '../helpers/ui'

const SMART_REVIEW_RULE = 'native-codewhale-smart-review'

codewhaleTest('uses the native Smart reviewer and restores its setting after reload', async ({ native }) => {
  const { page, modelScript } = native
  const agent = await currentNativeAgent(native)
  const file = join(agent.workingDir, '.env')
  expect(existsSync(file)).toBe(false)
  await modelScript.rule({
    name: SMART_REVIEW_RULE,
    when: { system: '^You are the Auto-Review guardian for a coding agent' },
    respond: { text: '{"risk_level":"low","decision":"allow","reason":"The isolated fixture write is reversible."}' },
  })
  await applyPermissionPreset(page, 'smart')
  for (const reload of [false, true]) {
    if (reload) {
      await page.reload()
      await waitForSettingsHydrated(page)
    }
    await expectNoNativeControl(native, { testId: 'control-banner', relatedProof: async () => {
      const reviews = (await modelScript.status()).ruleMatches[SMART_REVIEW_RULE] ?? 0
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
      const start = await modelScript.queue(
        { toolCalls: [writeToolCall(native.provider, id, { path: file, content })] },
        nativeTextStep(native, 'The native reviewer permitted the fixture write.'),
      )
      await sendMessage(page, modelScript.prompt('Run the scripted protected fixture write under Smart permissions.'))
      const status = await modelScript.waitForSteps(start + 2)
      await waitForAgentIdle(page)
      // Check the native result first. A denied write states its reason there.
      expect(nativeToolResult(await modelScript.requestAt(start + 1), id)).not.toMatch(/denied|not allowed/i)
      expect(readFileSync(file, 'utf8')).toBe(content)
      // Codewhale consults the guardian once for each held call. It never reuses an earlier verdict.
      expect(status.ruleMatches[SMART_REVIEW_RULE]).toBe(reviews + 1)
    } })
  }
})
