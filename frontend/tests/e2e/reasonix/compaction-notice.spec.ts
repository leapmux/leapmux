import { readFile } from 'node:fs/promises'
import { expect } from '@playwright/test'
import { isObject } from '../../../src/lib/jsonPick'
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
  await expectNoCompactionNotice(native, {
    relatedProof: () => exerciseReasonixCompactAsModelText(native),
  })
})

reasonixTest('shows automatic native compaction and keeps its notice after reload', async ({ page, leapmuxServer, modelScript }) => {
  await withPrivateNativeWorkspace(page, leapmuxServer, {
    prefix: 'rx-compact',
    workerName: 'Reasonix compaction test',
    providerAgent: REASONIX_AGENT,
    prepare: runDirectory => prepareCompactionReasonix(runDirectory, leapmuxServer.mockModelUrl),
    openAgent: (server, workspaceId, workingDir) => openAgentViaAPI(server, workspaceId, workingDir, agentOpenOptions(REASONIX_AGENT.provider)),
  }, async ({ server, workspaceId }) => {
    const native = await nativeContext({
      page,
      leapmuxServer: server,
      workspaceId,
      modelScript,
    })
    const before = await currentNativeAgent(native)
    // Native preflight estimates stored messages rather than reported usage.
    const padding = Array.from({ length: 650 }, (_, index) => `OLD_ASSISTANT_PADDING_${index} contains material for context compaction.`).join('\n')
    for (let index = 0; index < 5; index++) {
      const start = await modelScript.queue({
        text: `${modelScript.prompt(`Seed ${index}.`)}\n${padding}`,
        usage: {
          inputTokens: index === 4
            ? 45000
            : 10000,
          outputTokens: 6000,
        },
      })
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
    const continued = await modelScript.requestAt(finalStep)
    if (!isObject(continued.body))
      throw new TypeError('The native Reasonix request body is not an object.')
    const messages = continued.body.messages
    if (!Array.isArray(messages))
      throw new TypeError('The native Reasonix request has no messages.')
    const summaries = messages.filter(isObject).filter(message => message.role === 'user'
      && typeof message.content === 'string' && message.content.startsWith('<compaction-summary>\n'))
    expect(summaries).toHaveLength(1)
    const summary = summaries[0]!.content as string
    expect(summary).toContain('They were archived, not summarized: the record is complete and verbatim.')
    const archivePath = /^Archive: (\/[^\n]+)$/m.exec(summary)?.[1]
    if (!archivePath)
      throw new Error('The native Reasonix compaction notice has no archive path.')
    const archive = await readFile(archivePath, 'utf8')
    for (let index = 0; index < 5; index++)
      expect(archive).toContain(`Seed ${index}.`)
    expect(archive).toContain(padding)
    expect(JSON.stringify(continued.body)).not.toContain('Seed 0.')
    expect((await modelScript.status()).unexpectedRequests).toEqual([])
    expect((await currentNativeAgent(native)).agentSessionId).toBe(before.agentSessionId)
    await expectCompactionNoticeAfterReload(page, { count: 1 })
  })
})
