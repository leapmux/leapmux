import { expect } from '@playwright/test'
import { gooseTest } from '../goose-fixtures'
import { LIVE_CHILD_SPAWN_CALL_ID } from '../helpers/liveChildTranscript'
import { selectedAgentTabId } from '../helpers/nativeScenario'
import { nativeToolResultAt } from '../helpers/nativeToolExecution'
import { bashToolCall, gooseFinalOutputToolCall, spawnSubagentToolCall } from '../helpers/providerToolCalls'
import { expectRowBecomesFinal, openChildTabFromRow, requireRegistryRow } from '../helpers/subagentRegistry'
import { applyPermissionPreset, messageContents, sendMessage, subagentReportBubble, tabById, toolRows, waitForSettingsIdle } from '../helpers/ui'

gooseTest('opens a running child with its prompt and shows the report when it finishes', async ({ native }) => {
  const { page, modelScript, provider } = native
  const parentId = await selectedAgentTabId(page)
  await applyPermissionPreset(page, 'bypass')
  await waitForSettingsIdle(page)
  const gate = 'goose-live-child-held'
  const shellCall = 'goose-live-shell'
  const childPrompt = 'Run the live shell command and report the result.'
  const start = await modelScript.queue(
    {
      toolCalls: [
        spawnSubagentToolCall(provider, LIVE_CHILD_SPAWN_CALL_ID, {
          description: 'Run the live shell probe',
          prompt: modelScript.prompt(childPrompt),
        }),
      ],
    },
    {
      toolCalls: [
        bashToolCall(provider, shellCall, 'printf "goose-live-%s\\n" "$((40 + 2))"'),
      ],
    },
    {
      toolCalls: [
        gooseFinalOutputToolCall('live-child-final-goose', 'GOOSE_CHILD_LIVE_DONE'),
      ],
      gate,
    },
    { text: 'The child reported the probe output.' },
  )
  await sendMessage(page, modelScript.prompt('Delegate the live shell probe to a child.'))
  await modelScript.waitForGate(gate)
  expect(await nativeToolResultAt(modelScript, start + 2, shellCall)).toContain('goose-live-42')

  // Goose's ACP stream forwards the delegate updates and final output. The
  // child's shell result reaches its model without becoming a transcript row.
  const row = await requireRegistryRow(page)
  await expect(row).toHaveAttribute('data-status', 'running')
  await openChildTabFromRow(page, row)
  await expect(messageContents(page).filter({ hasText: childPrompt }).first()).toBeVisible()
  await expect(toolRows(page)).toHaveCount(0)
  await expect(subagentReportBubble(page, /GOOSE_CHILD_LIVE_DONE/)).toHaveCount(0)

  await modelScript.releaseGate(gate)
  await tabById(page, parentId).click()
  await modelScript.waitForSteps(start + 4)
  await expectRowBecomesFinal(page, row)

  // The finished child reports through its exported final output.
  await openChildTabFromRow(page, row)
  await expect(subagentReportBubble(page, /GOOSE_CHILD_LIVE_DONE/)).toBeVisible()
  await expect(toolRows(page)).toHaveCount(0)
})
