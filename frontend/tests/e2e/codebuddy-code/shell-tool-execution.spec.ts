import { expect } from '@playwright/test'
import { codebuddyTest } from '../codebuddy-fixtures'
import { cssAttributeValue } from '../helpers/cssAttribute'
import { exerciseShellToolExecution, runNativeToolTurn } from '../helpers/nativeToolExecution'
import { bashToolCall } from '../helpers/providerToolCalls'
import { assistantBubbles, railedRows } from '../helpers/ui'

codebuddyTest.describe('CodeBuddy Code tool execution', () => {
  codebuddyTest('runs a Bash tool and draws its span', async ({ native }) => {
    const { page } = native
    const call = bashToolCall(native.provider, 'call-1', 'echo hi')
    await runNativeToolTurn(native, { toolCalls: [call], prompt: 'Run echo hi.', answer: 'The command ran.' })

    await expect(assistantBubbles(page).filter({ hasText: 'The command ran.' }).first()).toBeVisible()
    // A tool call opens a span, so a row of this call draws a rail.
    await expect(railedRows(page).filter({ has: page.locator(`[data-tool-call-id="${cssAttributeValue(call.id)}"]`) }).first()).toBeVisible()
  })
})

codebuddyTest('runs successful and failed native commands with their actual output', async ({ native }) => {
  await exerciseShellToolExecution(native)
})
