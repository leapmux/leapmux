import { Buffer } from 'node:buffer'
import { existsSync, writeFileSync } from 'node:fs'
import { writeFile } from 'node:fs/promises'
import { join } from 'node:path'
import { AgentProvider } from '../../src/generated/proto/leapmux/v1/agent_pb'
import { backgroundBashToolCall, junieAnswerToolCall, junieSubagentSubmitToolCall, readToolCall, spawnSubagentToolCall } from './helpers/providerToolCalls'
import {
  expectRowBecomesFinal,
  openChildTabFromRow,
  requireRegistryRow,
} from './helpers/subagentRegistry'
import { assistantBubbles, messageContents, openWorkspace, sendMessage, tabById, userBubbles, waitForAgentIdle, waitForControlBanner } from './helpers/ui'
import { expect, JUNIE_E2E_SKIP_REASON, junieTest, openJunieAgent } from './junie-fixtures'

junieTest.skip(!!JUNIE_E2E_SKIP_REASON, JUNIE_E2E_SKIP_REASON || '')

const PROVIDER = AgentProvider.JUNIE

/** The task the child performs. A rule on the child's user turn answers it alone. */
const CHILD_TASK = 'Use the bundled Junie docs to explain where Junie stores session history.'
const CHILD_GATE = 'junie-docs-submit'
const CUSTOM_TASK = 'Read the marker file, then report its exact content.'
const CUSTOM_READ_MARKER = 'JUNIE_CUSTOM_READ_MARKER'
const CUSTOM_GATE = 'junie-custom-submit'

/** Housekeeping turns every Junie task answers before the main agent runs. */
function junieHousekeeping() {
  return [
    { name: 'junie-capability-filter', when: { system: 'capability filter agent' }, respond: { text: '' } },
    { name: 'junie-task-name', when: { system: 'task description summarizer' }, respond: { text: 'Subagent task' } },
    { name: 'junie-task-summary', when: { system: 'You are a task summarizer' }, respond: { text: '<summary>Junie stores sessions in Junie Home.</summary><title>Session history</title>' } },
  ]
}

/** Keep the native command open until this test writes the release file. */
function nativeBackgroundCommand(release: string, done: string): string {
  const source = `
const fs = require('node:fs')
const path = require('node:path')
const release = ${JSON.stringify(release)}
const done = ${JSON.stringify(done)}
let finished = false
function finish() {
  if (finished || !fs.existsSync(release)) return
  finished = true
  fs.writeFileSync(done, 'done')
  watcher.close()
}
const watcher = fs.watch(path.dirname(release), finish)
finish()
`
  const encoded = Buffer.from(source).toString('base64')
  return `node -e "eval(Buffer.from('${encoded}','base64').toString())"`
}

junieTest.describe('Junie subagents and background tasks', () => {
  // `spawn_subagent` blocks on the child and returns its result. The registry
  // row opens the child's transcript in a tab of its own.
  junieTest('routes the child task and report into a tab opened from the registry row', async ({ page, authenticatedEmptyWorkspace, leapmuxServer, modelScript }) => {
    const { agentId } = await openJunieAgent(leapmuxServer, authenticatedEmptyWorkspace.workspaceId)
    await openWorkspace(page, authenticatedEmptyWorkspace.workspaceId)
    const childPrompt = modelScript.prompt(CHILD_TASK)

    await modelScript.rule(...junieHousekeeping())
    await modelScript.rule({
      name: 'the docs child submits its answer',
      when: { system: 'You are the Junie documentation assistant', body: CHILD_TASK },
      respond: {
        gate: CHILD_GATE,
        toolCalls: [junieSubagentSubmitToolCall('junie-child-submit', '### Summary\n- JUNIE_CHILD_DONE: Junie keeps its session history in its home.\n### Changes\n- No files changed.\n### Verification\n- Read the bundled documentation.')],
      },
      once: true,
    })
    await modelScript.queue(
      {
        toolCalls: [spawnSubagentToolCall(PROVIDER, 'spawn-junie', {
          description: 'Find Junie session history',
          prompt: childPrompt,
        })],
      },
      { toolCalls: [junieAnswerToolCall('junie-root-answer', 'JUNIE_ROOT_DONE')] },
    )
    await sendMessage(page, modelScript.prompt('Delegate the Junie session history question, then report.'))
    await modelScript.waitForGate(CHILD_GATE)
    try {
      const row = await requireRegistryRow(page)
      await expect(row).toHaveAttribute('data-status', 'running')
      await expect(row).toContainText('junie-cli-docs')
      await openChildTabFromRow(page, row)
      await expect(userBubbles(page).filter({ hasText: CHILD_TASK })).toHaveCount(1)
    }
    finally {
      await modelScript.releaseGate(CHILD_GATE)
    }

    await modelScript.waitForSteps()
    await waitForAgentIdle(page, 120_000)
    await expect(assistantBubbles(page).filter({ hasText: 'JUNIE_CHILD_DONE' }).first()).toBeVisible()

    await tabById(page, agentId).click()
    await expect(assistantBubbles(page).filter({ hasText: 'JUNIE_ROOT_DONE' }).first()).toBeVisible()
    await expectRowBecomesFinal(page, await requireRegistryRow(page))
  })

  junieTest('streams a custom child read result into its tab before the final answer', async ({ page, authenticatedEmptyWorkspace, leapmuxServer, modelScript }) => {
    const { agentId, workingDir } = await openJunieAgent(leapmuxServer, authenticatedEmptyWorkspace.workspaceId)
    const note = join(workingDir, 'junie-child-note.txt')
    writeFileSync(note, `${CUSTOM_READ_MARKER}\n`)
    await openWorkspace(page, authenticatedEmptyWorkspace.workspaceId)

    await modelScript.rule(...junieHousekeeping())
    await modelScript.rule(
      {
        name: 'the custom Junie child reads the marker file',
        when: { system: 'You are the LeapMux test subagent', body: CUSTOM_TASK },
        respond: { toolCalls: [readToolCall(PROVIDER, 'junie-custom-read', note)] },
        once: true,
      },
      {
        name: 'the custom Junie child submits the marker',
        when: { system: 'You are the LeapMux test subagent', body: CUSTOM_READ_MARKER },
        respond: {
          gate: CUSTOM_GATE,
          toolCalls: [junieSubagentSubmitToolCall('junie-custom-submit', '### Summary\n- JUNIE_CUSTOM_CHILD_DONE: I read the marker file.\n### Changes\n- No files changed.\n### Verification\n- Read the marker file.')],
        },
        once: true,
      },
    )
    await modelScript.queue(
      {
        toolCalls: [spawnSubagentToolCall(PROVIDER, 'junie-custom-spawn', {
          description: 'Read the marker file',
          prompt: modelScript.prompt(`${CUSTOM_TASK}\nPath: ${note}`),
          agentType: 'leapmux-e2e-child',
        })],
      },
      { toolCalls: [junieAnswerToolCall('junie-custom-root', 'JUNIE_CUSTOM_ROOT_DONE')] },
    )
    await sendMessage(page, modelScript.prompt('Delegate the marker file to the custom child, then report.'))
    await modelScript.waitForSteps(1)
    const row = await requireRegistryRow(page)
    await expect(row).toContainText('leapmux-e2e-child')
    await modelScript.waitForGate(CUSTOM_GATE)
    try {
      await expect(row).toHaveAttribute('data-status', 'running')
      const childTabID = await openChildTabFromRow(page, row)
      await expect(userBubbles(page).filter({ hasText: CUSTOM_TASK })).toHaveCount(1)
      await expect(messageContents(page).filter({ hasText: CUSTOM_READ_MARKER }).first()).toBeVisible()
      await tabById(page, agentId).click()
      await expect(messageContents(page).filter({ hasText: CUSTOM_READ_MARKER })).toHaveCount(0)
      await tabById(page, childTabID).click()
    }
    finally {
      await modelScript.releaseGate(CUSTOM_GATE)
    }

    await modelScript.waitForSteps()
    await waitForAgentIdle(page, 120_000)
    await expect(assistantBubbles(page).filter({ hasText: 'JUNIE_CUSTOM_CHILD_DONE' }).first()).toBeVisible()
    const rows = await messageContents(page).allTextContents()
    const promptIndex = rows.findIndex(text => text.includes(CUSTOM_TASK))
    const readIndex = rows.findIndex(text => text.includes(CUSTOM_READ_MARKER))
    const answerIndex = rows.findIndex(text => text.includes('JUNIE_CUSTOM_CHILD_DONE'))
    expect(promptIndex).toBeGreaterThanOrEqual(0)
    expect(readIndex).toBeGreaterThan(promptIndex)
    expect(answerIndex).toBeGreaterThan(readIndex)

    await tabById(page, agentId).click()
    await expect(assistantBubbles(page).filter({ hasText: 'JUNIE_CUSTOM_ROOT_DONE' }).first()).toBeVisible()
    await expectRowBecomesFinal(page, await requireRegistryRow(page))
  })

  // Junie executes `background:true`, but ACP does not report a durable process
  // status after its terminal tool call ends. The Worker must not invent a
  // background shell row from the terminal card alone.
  junieTest('runs a native background command without a false shell registry row', async ({ page, authenticatedEmptyWorkspace, leapmuxServer, modelScript }) => {
    const { workingDir } = await openJunieAgent(leapmuxServer, authenticatedEmptyWorkspace.workspaceId, { brave_mode: 'off' })
    const release = join(workingDir, 'junie-background-release.signal')
    const done = join(workingDir, 'junie-background-done.txt')
    await openWorkspace(page, authenticatedEmptyWorkspace.workspaceId)

    await modelScript.rule(...junieHousekeeping())
    await modelScript.queue(
      { toolCalls: [backgroundBashToolCall(PROVIDER, 'junie-bg', nativeBackgroundCommand(release, done))] },
      { toolCalls: [junieAnswerToolCall('junie-bg-answer', 'I started the command in the background.')] },
    )
    await sendMessage(page, modelScript.prompt('Run the command in the background.'))
    await modelScript.waitForSteps(1)
    await waitForControlBanner(page)
    await page.getByTestId('control-allow-btn').filter({ visible: true }).click()
    await modelScript.waitForSteps()
    await waitForAgentIdle(page, 120_000)
    expect(existsSync(done)).toBe(false)
    await expect(page.locator('[data-testid="bg-task-row"][data-kind="shell"]')).toHaveCount(0)
    await writeFile(release, 'continue\n')
    await expect.poll(() => existsSync(done)).toBe(true)
    await expect(page.locator('[data-testid="bg-task-row"][data-kind="shell"]')).toHaveCount(0)
    await expect(assistantBubbles(page).filter({ hasText: 'I started the command' }).first()).toBeVisible()
  })
})
