import type { Page } from '@playwright/test'
import { existsSync, readFileSync, writeFileSync } from 'node:fs'
import { join } from 'node:path'
import { CLINE_SPAWN_WARNING } from '../../src/components/chat/providers/cline/spawnWarning'
import { CLINE_DECLINE_REASON } from '../../src/generated/contracts/cline-protocol'
import { AgentProvider } from '../../src/generated/proto/leapmux/v1/agent_pb'
import { CLINE_E2E_SKIP_REASON, clineTest, expect, offeredTools } from './cline-fixtures'
import { askUserQuestionToolCall, bashToolCall, exitPlanModeToolCall, readToolCall, spawnSubagentToolCall } from './helpers/providerToolCalls'
import {
  applyPermissionPreset,
  assistantBubbles,
  expectSettingsChip,
  messageBubbles,
  messageContents,
  openPlusMenu,
  sendMessage,
  waitForAgentIdle,
} from './helpers/ui'

/**
 * 242 — Cline control requests.
 *
 * The worker creates each Cline session with a policy that asks before every tool
 * call, and it answers each approval from the agent's own mode, as Cline's own CLI
 * does: Act lets reads, searches, fetches and questions through, and raises a banner
 * for every other call. A question is a call of Cline's question executor, which
 * the worker owns, so it raises the shared question banner. In Plan mode, the plan
 * tool raises the plan approval, and an approval rebuilds the session in Act mode
 * and continues the plan.
 */
clineTest.skip(!!CLINE_E2E_SKIP_REASON, CLINE_E2E_SKIP_REASON || '')

const PROVIDER = AgentProvider.CLINE

/** The control banner on screen. The chat renders each unmeasured row twice. */
function banner(page: Page) {
  return page.getByTestId('control-banner').filter({ visible: true })
}

/** The visible chat text, joined. */
async function chatText(page: Page): Promise<string> {
  return (await messageContents(page).allTextContents()).join(' ')
}

/**
 * The result that one tool call gave the model, read off the `tool` message of a
 * Chat Completions request. The whole body is not enough: the tool call repeats its
 * own arguments there, so each option label is in the body whatever the reader chose.
 */
function toolResult(body: unknown, toolCallId: string): string {
  const messages = (body as { messages?: { role?: string, tool_call_id?: string, content?: unknown }[] } | undefined)?.messages ?? []
  const results = messages.filter(message => message.role === 'tool' && message.tool_call_id === toolCallId)
  expect(results, `the request carries the result of ${toolCallId}`).toHaveLength(1)
  return JSON.stringify(results[0]!.content)
}

clineTest.describe('Cline control requests', () => {
  clineTest('runs a command after the reader allows it', async ({ askingClineWorkspace, page, modelScript }) => {
    void askingClineWorkspace
    await expectSettingsChip(page, 'Act')
    await modelScript.queue(
      { toolCalls: [bashToolCall(PROVIDER, 'allow-call', 'echo "cline-$((40 + 2))"')] },
      { text: 'The command ran.' },
    )
    await sendMessage(page, modelScript.prompt('Run the arithmetic command.'))
    // The call waits on the banner, so the second step waits too.
    await modelScript.waitForSteps(1)

    await expect(banner(page)).toContainText('echo "cline-$((40 + 2))"')
    await expect(banner(page)).toContainText('run_commands')
    await page.getByTestId('control-allow-btn').filter({ visible: true }).click()

    await modelScript.waitForSteps()
    await waitForAgentIdle(page, 180_000)
    await expect(banner(page)).toHaveCount(0)
    await expect.poll(() => chatText(page)).toContain('cline-42')
  })

  clineTest('refuses a command with the reader\'s reason, which reaches the model', async ({ askingClineWorkspace, page, modelScript }) => {
    const marker = join(askingClineWorkspace.workingDir, 'refused.txt')
    await modelScript.queue(
      { toolCalls: [bashToolCall(PROVIDER, 'deny-call', `printf refused > ${marker}`)] },
      { text: 'The command was refused.' },
    )
    await sendMessage(page, modelScript.prompt('Run the refused command.'))
    await modelScript.waitForSteps(1)
    await expect(banner(page)).toContainText(`printf refused > ${marker}`)

    // Text in the composer turns Deny into Send feedback, which refuses with it.
    await page.getByTestId('composer-editor').locator('.ProseMirror').fill('Use the clean target instead.')
    const deny = page.getByTestId('control-deny-btn').filter({ visible: true })
    await expect(deny).toHaveText('Send feedback')
    await deny.click()

    const status = await modelScript.waitForSteps()
    await waitForAgentIdle(page, 180_000)
    await expect(banner(page)).toHaveCount(0)
    // Cline hands the reason to the model as the call's error. The command never ran.
    const followUp = status.requests.find(request => request.stepIndex === 1)
    expect(JSON.stringify(followUp?.body)).toContain('Use the clean target instead.')
    await expect(messageBubbles(page).filter({ hasText: 'Use the clean target instead.' }).first()).toBeVisible()
    expect(existsSync(marker)).toBe(false)
  })

  clineTest('reads a file without a banner, as Cline\'s own CLI does in Act', async ({ askingClineWorkspace, page, modelScript }) => {
    const notes = join(askingClineWorkspace.workingDir, 'notes.txt')
    writeFileSync(notes, 'cline-safe-read\n')
    await modelScript.queue(
      { toolCalls: [readToolCall(PROVIDER, 'safe-read', notes)] },
      { text: 'I read the notes.' },
    )
    await sendMessage(page, modelScript.prompt('Read the notes.'))
    await modelScript.waitForSteps()
    await waitForAgentIdle(page, 180_000)

    await expect(banner(page)).toHaveCount(0)
    await expect.poll(() => chatText(page)).toContain('cline-safe-read')
  })

  clineTest('answers a question with the option the reader picks', async ({ askingClineWorkspace, page, modelScript }) => {
    void askingClineWorkspace
    await modelScript.queue(
      {
        toolCalls: [askUserQuestionToolCall(PROVIDER, 'cline-question', [{
          question: 'Which database?',
          header: 'Database',
          options: [{ label: 'Postgres', description: 'Relational' }, { label: 'Redis', description: 'In-memory' }],
        }])],
      },
      { text: 'Redis it is.' },
    )
    await sendMessage(page, modelScript.prompt('Ask me for a database.'))
    await modelScript.waitForSteps(1)
    await expect(banner(page)).toContainText('Which database?')
    // The SECOND option: a result that states the first option, or no option at
    // all, fails the check below.
    await page.locator('[data-testid="question-option-Redis"]:visible').click()
    const submit = page.locator('[data-testid="control-submit-btn"]:visible')
    await expect(submit).toBeEnabled()
    await submit.click()
    await expect(banner(page)).toHaveCount(0)

    const status = await modelScript.waitForSteps()
    await waitForAgentIdle(page, 180_000)
    // The worker answered Cline's question executor, and Cline gave the answer to the
    // model as the question's result.
    const followUp = status.requests.find(request => request.stepIndex === 1)
    const result = toolResult(followUp?.body, 'cline-question')
    expect(result).toContain('Redis')
    expect(result).not.toContain('Postgres')
    await expect(assistantBubbles(page).filter({ hasText: 'Redis it is.' })).toBeVisible()
  })

  clineTest('warns that an approved subagent asks nothing, and a refusal reaches the model', async ({ askingClineWorkspace, page, modelScript }) => {
    void askingClineWorkspace
    await modelScript.queue(
      {
        toolCalls: [spawnSubagentToolCall(PROVIDER, 'spawn-refused', {
          description: 'Refused helper',
          prompt: 'Never runs, because the reader refuses the spawn.',
        })],
      },
      { text: 'The subagent was refused.' },
    )
    await sendMessage(page, modelScript.prompt('Start a helper subagent.'))
    await modelScript.waitForSteps(1)
    await expect(banner(page)).toContainText('spawn_agent')
    await expect(banner(page)).toContainText(CLINE_SPAWN_WARNING)
    await page.getByTestId('control-deny-btn').filter({ visible: true }).click()

    const status = await modelScript.waitForSteps()
    await waitForAgentIdle(page, 180_000)
    await expect(banner(page)).toHaveCount(0)
    // A refusal with no words of the reader's gives the model LeapMux's own reason.
    const followUp = status.requests.find(request => request.stepIndex === 1)
    expect(JSON.stringify(followUp?.body)).toContain(CLINE_DECLINE_REASON.Tool)
  })

  clineTest('runs every call without a banner in Auto-approve, which the Bypass shortcut selects', async ({ askingClineWorkspace, page, modelScript }) => {
    const marker = join(askingClineWorkspace.workingDir, 'bypass.txt')
    await expectSettingsChip(page, 'Act')
    // Cline has no mode that asks for the risky calls alone, so it offers no Smart
    // shortcut. Bypass selects Auto-approve, which applies to the next call at once.
    const menu = await openPlusMenu(page)
    await expect(menu.getByTestId('composer-smart-permissions')).toHaveCount(0)
    await expect(menu.getByTestId('composer-bypass-permissions')).toBeVisible()
    await page.keyboard.press('Escape')
    await applyPermissionPreset(page, 'bypass')
    await expectSettingsChip(page, 'Auto-approve')

    await modelScript.queue(
      { toolCalls: [bashToolCall(PROVIDER, 'bypass-call', `printf bypass > ${marker}`)] },
      { text: 'The command ran without a banner.' },
    )
    await sendMessage(page, modelScript.prompt('Run the bypass command.'))
    await modelScript.waitForSteps()
    await waitForAgentIdle(page, 180_000)

    await expect(banner(page)).toHaveCount(0)
    expect(readFileSync(marker, 'utf8')).toBe('bypass')
  })

  clineTest('approves the plan, switches to Act, and continues the plan', async ({ planningClineWorkspace, page, modelScript }) => {
    const note = join(planningClineWorkspace.workingDir, 'note.txt')
    await expectSettingsChip(page, 'Plan')

    // Cline's plan mode presents the plan in an answer and waits for the reader's
    // reply. The model calls the plan tool only after that reply.
    await modelScript.queue({ text: 'Plan:\n1. Create note.txt.\n2. Verify it.' })
    await sendMessage(page, modelScript.prompt('Plan how to write the note.'))
    await modelScript.waitForSteps()
    await waitForAgentIdle(page, 180_000)

    await modelScript.queue(
      { toolCalls: [exitPlanModeToolCall(PROVIDER, 'cline-plan', '')] },
      // The continuation prompt that the worker sends after the switch, in Act mode.
      { text: 'Starting the approved plan.' },
    )
    await sendMessage(page, modelScript.prompt('Looks good, go ahead.'))
    await modelScript.waitForSteps(2)
    await expect(banner(page)).toBeVisible()
    await page.getByTestId('plan-approve-btn').filter({ visible: true }).click()
    await expect(banner(page)).toHaveCount(0)

    const status = await modelScript.waitForSteps()
    await waitForAgentIdle(page, 180_000)
    await expectSettingsChip(page, 'Act')
    await expect(assistantBubbles(page).filter({ hasText: 'Starting the approved plan.' })).toBeVisible()

    // The plan turn offered the plan tool and no editor. The new Act session offers
    // the editor, holds the conversation, and reads the worker's continuation prompt.
    const planTurn = status.requests.find(request => request.stepIndex === 1)?.body
    expect(offeredTools(planTurn)).toContain('switch_to_act_mode')
    expect(offeredTools(planTurn)).not.toContain('editor')
    const actTurn = status.requests.find(request => request.stepIndex === 2)?.body
    expect(offeredTools(actTurn)).toContain('editor')
    expect(offeredTools(actTurn)).not.toContain('switch_to_act_mode')
    expect(JSON.stringify(actTurn)).toContain('Create note.txt.')
    expect(JSON.stringify(actTurn)).toContain('The user approved switching to act mode.')
    expect(existsSync(note)).toBe(false)
  })

  clineTest('keeps Plan mode when the reader rejects the plan with feedback', async ({ planningClineWorkspace, page, modelScript }) => {
    void planningClineWorkspace
    await modelScript.queue(
      { toolCalls: [exitPlanModeToolCall(PROVIDER, 'cline-plan-rejected', '')] },
      { text: 'I will split the plan.' },
    )
    await sendMessage(page, modelScript.prompt('The plan is fine, switch to act mode.'))
    await modelScript.waitForSteps(1)
    await expect(banner(page)).toBeVisible()
    await page.getByTestId('composer-editor').locator('.ProseMirror').fill('Split the plan into two steps first.')
    await page.getByTestId('plan-reject-btn').filter({ visible: true }).click()
    await expect(banner(page)).toHaveCount(0)

    const status = await modelScript.waitForSteps()
    await waitForAgentIdle(page, 180_000)
    await expectSettingsChip(page, 'Plan')
    // The refusal reached the model as the plan tool's error, and the session still
    // offers the plan tool.
    const followUp = status.requests.find(request => request.stepIndex === 1)?.body
    expect(JSON.stringify(followUp)).toContain('Split the plan into two steps first.')
    expect(offeredTools(followUp)).toContain('switch_to_act_mode')
    expect(offeredTools(followUp)).not.toContain('editor')
  })
})
