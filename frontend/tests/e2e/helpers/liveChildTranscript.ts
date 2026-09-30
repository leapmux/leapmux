import type { Page } from '@playwright/test'
import type { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import type { MockModelMatcher, MockModelStep } from './mockModelScript'
import type { ModelScript } from './modelScriptFixture'
import { randomUUID } from 'node:crypto'
import { writeFileSync } from 'node:fs'
import { basename, join } from 'node:path'
import { expect } from '@playwright/test'
import { readToolCall, spawnSubagentToolCall } from './providerToolCalls'
import { openChildTabFromRow, requireRegistryRow } from './subagentRegistry'
import { assistantBubbles, messageContents, sendMessage, tabById, userBubbles } from './ui'

export interface LiveChildSpec {
  provider: AgentProvider
  childWhen: MockModelMatcher
  childTask: string
  parentTask: string
  childResponse?: Omit<MockModelStep, 'gate'>
  holdParentAnswer?: boolean
  background?: boolean
  allowPaused?: boolean
  toolProof?: { workingDir: string }
}

/** Hold a child model answer while its prompt or tool result is visible in a running child tab. */
export async function exerciseLiveChildTranscript(page: Page, modelScript: ModelScript, spec: LiveChildSpec): Promise<void> {
  const gate = `live-child-${spec.provider}`
  const parentGate = spec.holdParentAnswer ? `live-parent-${spec.provider}` : undefined
  const filePath = spec.toolProof ? join(spec.toolProof.workingDir, `live-child-${spec.provider}.txt`) : undefined
  const marker = spec.toolProof ? `CHILDREAD${randomUUID().replaceAll('-', '')}` : undefined
  const parentTabID = spec.toolProof
    ? await page.locator('[data-testid="tab"][data-tab-type="agent"]').first().getAttribute('data-tab-id') ?? ''
    : ''
  if (filePath && marker) {
    expect(parentTabID).not.toBe('')
    writeFileSync(filePath, `${marker}\n`)
  }
  await modelScript.rule({
    name: filePath ? 'the child reads its marker file' : 'the held child answer',
    when: spec.childWhen,
    respond: filePath
      ? { toolCalls: [readToolCall(spec.provider, 'live-child-read', filePath)] }
      : { ...(spec.childResponse ?? { text: 'CHILD_LIVE_DONE' }), gate },
    once: true,
  })
  if (marker) {
    await modelScript.rule({
      name: 'the held child answer after the read',
      when: { body: marker },
      respond: { text: 'CHILD_LIVE_DONE', gate },
      once: true,
    })
  }
  await modelScript.queue(
    { toolCalls: [spawnSubagentToolCall(spec.provider, 'spawn-live-child', {
      description: 'Answer the live child task',
      prompt: modelScript.prompt(spec.childTask),
      ...(spec.background === undefined ? {} : { background: spec.background }),
    })] },
    { text: 'The child task ended.', ...(parentGate ? { gate: parentGate } : {}) },
  )
  await sendMessage(page, modelScript.prompt(spec.parentTask))
  await modelScript.waitForGate(gate)
  try {
    const row = await requireRegistryRow(page)
    await expect(row).toHaveAttribute('data-status', spec.allowPaused ? /^(?:running|paused)$/ : 'running')
    await expect.poll(async () => await row.getAttribute('data-child-agent-id') ?? '').not.toBe('')
    const childTabID = await openChildTabFromRow(page, row)
    await expect(userBubbles(page).filter({ hasText: spec.childTask }).first()).toBeVisible()
    await expect(assistantBubbles(page).filter({ hasText: 'CHILD_LIVE_DONE' })).toHaveCount(0)
    if (filePath && marker) {
      await expect(page.locator('[data-tool-message]:visible').filter({ hasText: basename(filePath) }).first()).toBeVisible()
      await expect(messageContents(page).filter({ hasText: marker }).first()).toBeVisible()
      await tabById(page, parentTabID).click()
      await expect(messageContents(page).filter({ hasText: marker })).toHaveCount(0)
      await tabById(page, childTabID).click()
    }
  }
  finally {
    await modelScript.releaseGate(gate)
    if (parentGate) {
      try {
        await modelScript.waitForGate(parentGate)
      }
      finally {
        if ((await modelScript.status()).pendingGates.includes(parentGate)) {
          await modelScript.releaseGate(parentGate)
        }
      }
    }
  }
  await modelScript.waitForSteps()
}
