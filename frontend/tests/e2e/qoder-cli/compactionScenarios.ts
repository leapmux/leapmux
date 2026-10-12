import type { NativeScenarioContext } from '../helpers/nativeScenario'
import { expect } from '@playwright/test'
import { compactionNoticeRow, expectCompactionNotice } from '../helpers/compaction'
import { CLAUDE_SUMMARIZER_PATTERN } from '../helpers/manualCompaction'
import { sendMessage, visibleOnly, waitForAgentIdle } from '../helpers/ui'

/** Exercise the actual native compaction path and preserve its context assertions. */
export async function exerciseCompletedManualCompaction(context: NativeScenarioContext): Promise<void> {
  const { page, modelScript } = context

  const oldMarker = 'QODER_OLD_CONTEXT_MARKER'
  const summaryMarker = 'QODER_COMPACTED_CONTEXT_MARKER'
  const olderAnswer = Array.from({ length: 3000 }, (_, index) => `${oldMarker} item ${index} records detail ${index * 7}.`).join(' ')
  await modelScript.rule({
    name: 'qoder manual summary',
    // Qoder CLI derives from Claude Code, and its summarizer prompt holds the same directive.
    when: { body: CLAUDE_SUMMARIZER_PATTERN },
    respond: {
      text: `<analysis>Keep only the task state.</analysis><summary>${summaryMarker} The prior work recorded the task state and recent decisions.</summary>`,
      usage: { inputTokens: 8000, outputTokens: 40 },
    },
  })
  for (let turn = 0; turn < 4; turn++) {
    // Qoder compares the new history with reported input tokens. The mock's
    // default one-token count makes a real summary look larger than its source.
    const step = await modelScript.queue({
      text: turn === 0 ? olderAnswer : `Recent task answer ${turn}.`,
      usage: { inputTokens: 6000 + turn * 500, outputTokens: turn === 0 ? 5000 : 50 },
    })
    await sendMessage(page, modelScript.prompt(`Record Qoder task turn ${turn}.`))
    await modelScript.waitForSteps(step + 1)
    await waitForAgentIdle(page)
  }

  await sendMessage(page, '/compact')
  await expect.poll(async () => (await modelScript.status()).ruleMatches['qoder manual summary'] ?? 0).toBeGreaterThan(0)
  await waitForAgentIdle(page)
  await expectCompactionNotice(page)

  const next = await modelScript.queue({ text: 'The compacted task continued.' })
  await sendMessage(page, modelScript.prompt('Continue after the native compaction.'))
  await modelScript.waitForSteps(next + 1)
  await waitForAgentIdle(page)
  const nextBody = JSON.stringify((await modelScript.requestAt(next)).body)
  expect(nextBody).toContain(summaryMarker)
  expect(nextBody).not.toContain(oldMarker)
}

/** Exercise the actual native compaction path and preserve its context assertions. */
export async function exerciseFailedManualCompaction(context: NativeScenarioContext): Promise<void> {
  const { page, modelScript } = context

  const step = await modelScript.queue({ text: 'Ready to compact.' })
  await sendMessage(page, modelScript.prompt('Reply once, then I will compact.'))
  await modelScript.waitForSteps(step + 1)
  await waitForAgentIdle(page)

  await modelScript.fallback({ text: 'Earlier work summarized.' })
  await sendMessage(page, '/compact')
  await waitForAgentIdle(page)
  const status = await modelScript.status()
  expect(status.requests.some(request => request.fallback)).toBe(true)
  await expect(visibleOnly(page.getByText('Turn failed', { exact: false })).first()).toBeVisible()
  await expect(compactionNoticeRow(page)).toHaveCount(0)
  await expect(visibleOnly(page.getByText('LeapMux has no display for this row', { exact: true }))).toHaveCount(0)
}
