import type { Page } from '@playwright/test'
import type { ModelScript } from './modelScriptFixture'
import { expect } from '@playwright/test'
import { assistantBubbles, messageBubbles, sendMessage, waitForAgentIdle } from './ui'

type ManualCompactionProof = ({ summaryRequestMarker: string, completionText?: never } | { completionText: string, summaryRequestMarker?: never }) & {
  summary?: string
  reportedInputTokens?: number
}

export const MANUAL_COMPACTION_MARKER = 'CEDAR_MANUAL_SUMMARY'
export const MANUAL_COMPACTION_SUMMARY = [
  `${MANUAL_COMPACTION_MARKER}: the cedar marker belongs to the older work.`,
  ...Array.from({ length: 30 }, (_, index) => `Earlier item ${index} records cedar detail ${index * 7} for the next assistant.`),
].join(' ')

const olderContext = Array.from({ length: 300 }, (_, index) => `OLDER_CONTEXT item ${index}: cedar detail ${index * 7}.`).join(' ')
const olderContextMarker = 'OLDER_CONTEXT item 0:'

/** Seed six conversation messages before the native compaction request. */
export async function seedManualCompactionConversation(page: Page, modelScript: ModelScript, reportedInputTokens?: number): Promise<void> {
  const turns = [
    ['Record the older context.', olderContext],
    ['Record a newer note.', 'The newer note is about the parser.'],
    ['Record the current note.', 'The current note is about the tests.'],
  ] as const
  for (const [index, [prompt, answer]] of turns.entries()) {
    await modelScript.queue({
      text: answer,
      ...(reportedInputTokens !== undefined ? { usage: { inputTokens: reportedInputTokens + index * 500, outputTokens: Math.max(1, Math.ceil(answer.length / 4)) } } : {}),
    })
    await sendMessage(page, modelScript.prompt(prompt))
    const status = await modelScript.waitForSteps()
    if (index === 1) {
      const request = status.requests.find(request => request.stepIndex === index)
      expect(request, 'the newer turn reached the model').toBeDefined()
      expect(JSON.stringify(request?.body)).toContain(olderContextMarker)
    }
    await waitForAgentIdle(page)
  }
}

/** Prove that the native summary replaces old context in the next turn. */
export async function exerciseManualCompaction(page: Page, modelScript: ModelScript, proof: ManualCompactionProof): Promise<void> {
  await seedManualCompactionConversation(page, modelScript, proof.reportedInputTokens)
  await modelScript.fallback({ text: proof.summary ?? MANUAL_COMPACTION_SUMMARY })
  await sendMessage(page, '/compact')
  await expect.poll(async () => (await modelScript.status()).requests.some(request => request.fallback)).toBe(true)
  await waitForAgentIdle(page)
  if (proof.summaryRequestMarker !== undefined) {
    const marker = proof.summaryRequestMarker
    const requests = (await modelScript.status()).requests.filter(request => request.fallback)
    expect(requests.some(request => (JSON.stringify(request.body) ?? '').includes(marker)), 'a native summary request reached the model').toBe(true)
  }
  else {
    await expect(messageBubbles(page).filter({ hasText: proof.completionText }).first()).toBeVisible()
  }

  const answer = 'The compacted session continued.'
  await modelScript.queue({ text: answer })
  await sendMessage(page, modelScript.prompt('Continue after the context summary.'))
  const status = await modelScript.waitForSteps()
  await waitForAgentIdle(page)
  const nextRequest = status.requests.find(request => request.stepIndex === 3)
  expect(nextRequest, 'the follow-up prompt reached the model').toBeDefined()
  const nextBody = JSON.stringify(nextRequest?.body) ?? ''
  expect(nextBody).toContain(MANUAL_COMPACTION_MARKER)
  expect(nextBody).not.toContain(olderContextMarker)
  await expect(assistantBubbles(page).filter({ hasText: answer }).first()).toBeVisible()
}
