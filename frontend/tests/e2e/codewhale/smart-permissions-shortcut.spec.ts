import { existsSync, readFileSync } from 'node:fs'
import { join } from 'node:path'
import { expect } from '@playwright/test'
import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { codewhaleTest } from '../codewhale-fixtures'
import { expectNoNativeControl } from '../helpers/nativeControlObservation'
import { currentNativeAgent } from '../helpers/nativeScenario'
import { nativeToolResult } from '../helpers/nativeToolResult'
import { writeToolCall } from '../helpers/providerToolCalls'
import { applyPermissionPreset, sendMessage, waitForAgentIdle, waitForSettingsHydrated } from '../helpers/ui'

const SMART_REVIEW_RULE = 'native-codewhale-smart-review'

codewhaleTest('uses the native Smart reviewer and restores its setting after reload', async ({ authenticatedCodewhaleWorkspace, page, modelScript, leapmuxServer }) => {
  const context = { page, modelScript, leapmuxServer, workspaceId: authenticatedCodewhaleWorkspace.workspaceId, provider: AgentProvider.CODEWHALE }
  const agent = await currentNativeAgent(context)
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
    await expectNoNativeControl(context, { testId: 'control-banner', relatedProof: async () => {
      const before = await modelScript.status()
      const start = before.stepCount
      const reviews = before.ruleMatches[SMART_REVIEW_RULE] ?? 0
      // The guardian request holds only the proposed call and Codewhale's own
      // observations. It never holds the conversation, so it never holds the
      // marked prompt (`build_reviewer_context` in Codewhale's
      // `tui/auto_review.rs`). The written content is the one part of that
      // request that this test controls, so the content carries the scenario
      // marker. Without the marker, the guardian request reaches the ambient
      // scenario, which refuses it, and Codewhale denies the write (fail closed).
      const content = `${modelScript.prompt(reload ? 'NATIVE_SMART_RESTORED=42' : 'NATIVE_SMART_APPLIED=42')}\n`
      const id = `native-smart-review-${start}`
      await modelScript.queue(
        { toolCalls: [writeToolCall(AgentProvider.CODEWHALE, id, { path: file, content })] },
        { text: 'The native reviewer permitted the fixture write.' },
      )
      await sendMessage(page, modelScript.prompt('Run the scripted protected fixture write under Smart permissions.'))
      const status = await modelScript.waitForSteps(start + 2)
      await waitForAgentIdle(page)
      // Check the native result first. A denied write states its reason there.
      expect(nativeToolResult(status.requests.find(request => request.stepIndex === start + 1), id)).not.toMatch(/denied|not allowed/i)
      expect(readFileSync(file, 'utf8')).toBe(content)
      // Codewhale consults the guardian once for each held call. It never reuses an earlier verdict.
      expect(status.ruleMatches[SMART_REVIEW_RULE]).toBe(reviews + 1)
    } })
  }
})
