import { expect } from '@playwright/test'
import { agentOpenOptions } from '../agentSettings'
import { openAgentViaAPI } from '../helpers/api'
import { expectCompactionNoticeAfterReload } from '../helpers/compaction'
import { currentNativeAgent } from '../helpers/nativeScenario'
import { withPrivateNativeWorkspace } from '../helpers/privateNativeWorkspace'
import { uniqueMarker } from '../helpers/shellArguments'
import { assistantBubbles, sendMessage, waitForAgentIdle } from '../helpers/ui'
import { expectNoCompactionNotice } from '../helpers/unsupportedCompaction'
import { reasonixTest } from '../reasonix-fixtures'
import { exerciseReasonixCompactAsModelText, prepareCompactionReasonix } from './compactionScenario'
import { nativeContext, REASONIX_AGENT } from './scenarios'

reasonixTest('retains native context without a compaction notice after the slash command', async ({ native }) => {
  await expectNoCompactionNotice(native, { relatedProof: () => exerciseReasonixCompactAsModelText(native) })
})

reasonixTest('shows automatic native compaction and keeps its notice after reload', async ({ page, leapmuxServer, modelScript }) => {
  await withPrivateNativeWorkspace(page, leapmuxServer, {
    prefix: 'rx-compact',
    workerName: 'Reasonix compaction test',
    providerAgent: REASONIX_AGENT,
    prepare: runDirectory => prepareCompactionReasonix(runDirectory, leapmuxServer.mockModelUrl),
    openAgent: (server, workspaceId, workingDir) => openAgentViaAPI(server, workspaceId, workingDir, agentOpenOptions(REASONIX_AGENT.provider)),
  }, async ({ server, workspaceId }) => {
    const native = await nativeContext({ page, leapmuxServer: server, workspaceId, modelScript })
    const before = await currentNativeAgent(native)
    const summaryMarker = `REASONIX_NATIVE_SUMMARY_${uniqueMarker()}`
    const rule = 'the native context summary'
    await modelScript.rule({
      name: rule,
      when: { system: String.raw`^You are compacting the earlier part of a coding agent's conversation to save context\.`, lastMessage: { role: 'user', text: String.raw`^\[assistant\]\nSeed 0\.` } },
      respond: { text: `## Goal\nContinue the compaction test.\n## Standing facts & constraints\n${summaryMarker}`, usage: { inputTokens: 10000, outputTokens: 80 } },
      once: true,
    })
    const padding = Array.from({ length: 400 }, (_, index) => `OLD_ASSISTANT_PADDING_${index} contains material for context compaction.`).join('\n')
    for (let index = 0; index < 5; index++) {
      const start = await modelScript.queue({ text: `${modelScript.prompt(`Seed ${index}.`)}\n${padding}`, usage: { inputTokens: index === 4 ? 45000 : 10000, outputTokens: 6000 } })
      await sendMessage(page, modelScript.prompt(`Return scripted context preparation turn ${index}.`))
      await modelScript.waitForSteps(start + 1)
      await expect(assistantBubbles(page).filter({ hasText: `Seed ${index}.` }).first()).toBeVisible()
      await waitForAgentIdle(page)
    }
    const finalAnswer = `REASONIX_COMPACTION_COMPLETE_${uniqueMarker()}`
    const finalStep = await modelScript.queue({ text: finalAnswer })
    await sendMessage(page, modelScript.prompt('Continue after native automatic context compaction.'))
    await modelScript.waitForSteps(finalStep + 1)
    await expect(assistantBubbles(page).filter({ hasText: finalAnswer })).toBeVisible()
    await waitForAgentIdle(page)
    const status = await modelScript.status()
    expect(status.ruleMatches[rule]).toBe(1)
    const summary = status.requests.find(request => request.rule === rule)
    if (!summary)
      throw new Error('Reasonix produced no native summary request.')
    expect(JSON.stringify(summary.body)).toContain('Seed 0.')
    const continued = await modelScript.requestAt(finalStep)
    expect(JSON.stringify(continued.body)).toContain(summaryMarker)
    expect(JSON.stringify(continued.body)).not.toContain('Seed 0.')
    expect((await currentNativeAgent(native)).agentSessionId).toBe(before.agentSessionId)
    await expectCompactionNoticeAfterReload(page, { count: 1 })
  })
})
