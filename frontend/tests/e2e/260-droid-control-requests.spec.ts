import type { Page } from '@playwright/test'
import type { MockModelRequestRecord } from './helpers/mockModelScript'
import { readFileSync, writeFileSync } from 'node:fs'
import { join } from 'node:path'
import { AgentProvider } from '../../src/generated/proto/leapmux/v1/agent_pb'
import { isObject } from '../../src/lib/jsonPick'
import { DROID_E2E_SKIP_REASON, DROID_TITLE_RULE, droidTest, expect } from './droid-fixtures'
import { nativeToolResult } from './helpers/nativeToolResult'
import { askUserQuestionToolCall, editToolCall } from './helpers/providerToolCalls'
import { sendMessage, waitForAgentIdle } from './helpers/ui'

/**
 * 260 — Factory Droid control requests.
 *
 * The worker opens the session in Default autonomy, so Droid raises a
 * `droid.request_permission` banner before a tool that changes something. The
 * reader's Allow answers with `proceed_once` and the call runs. A question is a
 * call of Droid's `AskUser` tool, which reaches the worker as `droid.ask_user`
 * and draws the shared question banner.
 */
droidTest.skip(!!DROID_E2E_SKIP_REASON, DROID_E2E_SKIP_REASON || '')

/** The control banner on screen. The chat renders each unmeasured row twice. */
function banner(page: Page) {
  return page.getByTestId('control-banner').filter({ visible: true })
}

const PROVIDER = AgentProvider.DROID

function nativeDroidCallId(request: MockModelRequestRecord | undefined, toolName: string, marker: string): string {
  const body = isObject(request?.body) ? request.body : null
  const messages = Array.isArray(body?.messages) ? body.messages : []
  const calls = messages
    .filter(isObject)
    .flatMap(message => Array.isArray(message.tool_calls) ? message.tool_calls.filter(isObject) : [])
    .filter(call => isObject(call.function) && call.function.name === toolName && typeof call.id === 'string' && call.id.includes(marker))
  expect(calls, `the request has one native ${toolName} call`).toHaveLength(1)
  const callId = calls[0]?.id
  if (typeof callId !== 'string')
    throw new Error(`Droid gave no native ID to its ${toolName} call`)
  return callId
}

droidTest.describe('Factory Droid control requests', () => {
  droidTest('runs a command after the reader allows it', async ({ askingDroidWorkspace, page, modelScript }) => {
    const note = join(askingDroidWorkspace.workingDir, 'notes.txt')
    writeFileSync(note, 'a')
    // Droid's `normal` autonomy asks before an `Edit` (a write), and
    // auto-approves `Execute` (a read-ish shell call). Script the call it
    // really asks for.
    await modelScript.rule(DROID_TITLE_RULE)
    await modelScript.queue(
      { toolCalls: [editToolCall(PROVIDER, 'allow-call', { path: 'notes.txt', before: 'a', after: 'b' })] },
      { text: 'The edit landed.' },
    )
    await sendMessage(page, modelScript.prompt('Edit the note.'))
    // The call waits on the banner, so the second step waits too.
    await modelScript.waitForSteps(1)

    await expect(banner(page)).toContainText('Edit')
    expect(readFileSync(note, 'utf8')).toBe('a')
    await page.getByTestId('control-allow-btn').filter({ visible: true }).click()

    const status = await modelScript.waitForSteps()
    await waitForAgentIdle(page, 180_000)
    await expect(banner(page)).toHaveCount(0)
    expect(readFileSync(note, 'utf8')).toBe('b')
    const followUp = status.requests.find(request => request.stepIndex === 1)
    const result = nativeToolResult(followUp, nativeDroidCallId(followUp, 'Edit', 'allow-call'))
    expect(result).not.toMatch(/error|denied/i)
  })

  droidTest('keeps the command from running after the reader denies it', async ({ askingDroidWorkspace, page, modelScript }) => {
    const note = join(askingDroidWorkspace.workingDir, 'notes.txt')
    writeFileSync(note, 'a')
    await modelScript.rule(DROID_TITLE_RULE)
    // A cancel ends the turn: Droid runs no follow-up model call, so one step
    // is the whole script.
    await modelScript.queue(
      { toolCalls: [editToolCall(PROVIDER, 'deny-call', { path: 'notes.txt', before: 'a', after: 'b' })] },
    )
    await sendMessage(page, modelScript.prompt('Edit the note.'))
    await modelScript.waitForSteps(1)

    expect(readFileSync(note, 'utf8')).toBe('a')
    await page.getByTestId('control-deny-btn').filter({ visible: true }).click()
    await waitForAgentIdle(page, 180_000)

    await expect(banner(page)).toHaveCount(0)
    expect(readFileSync(note, 'utf8')).toBe('a')
  })

  droidTest('answers a question through the shared question banner', async ({ askingDroidWorkspace, page, modelScript }) => {
    void askingDroidWorkspace
    await modelScript.rule(DROID_TITLE_RULE)
    await modelScript.queue(
      {
        toolCalls: [askUserQuestionToolCall(PROVIDER, 'ask-1', [
          { question: 'Which color do you prefer?', header: 'Color', options: [{ label: 'Blue', description: 'The color blue' }, { label: 'Red', description: 'The color red' }] },
        ])],
      },
      { text: 'The answer was recorded.' },
    )
    await sendMessage(page, modelScript.prompt('Ask me a question.'))
    await modelScript.waitForSteps(1)

    await expect(banner(page)).toContainText('Which color do you prefer?')
    await page.getByTestId('question-option-Red').filter({ visible: true }).first().click()
    await page.getByTestId('control-submit-btn').filter({ visible: true }).click()

    const status = await modelScript.waitForSteps()
    await waitForAgentIdle(page, 180_000)
    const followUp = status.requests.find(request => request.stepIndex === 1)
    const nativeCallId = nativeDroidCallId(followUp, 'AskUser', 'ask-1')
    const answer = nativeToolResult(followUp, nativeCallId)
    expect(answer).toContain('Red')
    expect(answer).not.toContain('Blue')
    await expect(banner(page)).toHaveCount(0)
  })
})
