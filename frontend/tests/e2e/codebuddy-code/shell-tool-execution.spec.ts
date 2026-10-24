import { expect } from '@playwright/test'
import { codebuddyTest } from '../codebuddy-fixtures'
import { cssAttributeValue } from '../helpers/cssAttribute'
import { expandNativeResultView } from '../helpers/nativeResultView'
import { exerciseShellToolExecution, runNativeToolTurn } from '../helpers/nativeToolExecution'
import { nativeToolCallArguments, nativeToolResultContent } from '../helpers/nativeToolResult'
import { bashToolCall } from '../helpers/providerToolCalls'
import { assistantBubbles, railedRows, toolCallRow } from '../helpers/ui'
import { readCodeBuddyShellResult } from './shellResult'

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

codebuddyTest('runs successful and failed native commands with their actual output', async ({ native }, testInfo) => {
  // The CLI can close its pipes before stderr arrives. Preserve its exact empty
  // failure record in that case, and retain the marker proof when stderr arrives.
  await exerciseShellToolExecution(native, {
    readResult: async (request, command) => {
      const evidence = readCodeBuddyShellResult(request, command)
      await testInfo.attach(`codebuddy-native-shell-result-${command.exitCode}`, {
        body: JSON.stringify({ command, evidence }),
        contentType: 'application/json',
      })
      return evidence
    },
  })
})

codebuddyTest('preserves the native empty failure record before and after reload', async ({ native }) => {
  const call = bashToolCall(native.provider, 'empty-shell-failure', 'exit 7')
  const { resultRequest } = await runNativeToolTurn(native, { toolCalls: [call], prompt: 'Run the empty failure command.', answer: 'The empty failure was recorded.' })
  expect(nativeToolCallArguments(resultRequest, call.id)).toMatchObject({ name: 'Bash', arguments: { command: 'exit 7' } })
  const record = nativeToolResultContent(resultRequest, call.id)
  expect(record).toBe('Stdout: (empty)\nStderr: (empty)\nExit Code: 7\nSignal: (none)')
  if (typeof record !== 'string')
    throw new Error('The native empty failure result is not text.')
  for (const reload of [false, true]) {
    if (reload)
      await native.page.reload()
    const row = toolCallRow(native.page, call.id)
    await expect(row).toHaveCount(1)
    await expandNativeResultView(row)
    const preview = row.locator('[data-tool-output-preview]')
    await expect(preview).toHaveCount(1)
    expect(await preview.textContent()).toBe(record)
  }
})
