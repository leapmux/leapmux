import { AgentProvider } from '../../src/generated/proto/leapmux/v1/agent_pb'
import { agentOpenOptions, agentSettings } from './agentSettings'
import { expect, test } from './fixtures'
import { openAgentViaAPI } from './helpers/api'
import { withMockModelScenario } from './helpers/mockModelScenario'
import { askUserQuestionToolCall, bashToolCall, editToolCall, exitPlanModeToolCall, readToolCall, spawnSubagentToolCall } from './helpers/providerToolCalls'
import { createTestDirectory } from './helpers/runDirectory'
import { withMockPiModel } from './helpers/scriptedPiModel'
import { expandGoalsAndTodosSection, expectGoalStatus, goalAction, listAgents, openGoalMenu } from './helpers/subagentRegistry'
import { applyPermissionPreset, expectSettingsChip, openWorkspace, sendMessage, waitForAgentIdle } from './helpers/ui'
import { zcodeTest } from './zcode-fixtures'

/**
 * NO SEEDING. This file used to write rows straight into the worker database
 * with `INSERT INTO messages`, so a renderer case could state a persisted shape
 * directly. Every such test was removed: the shapes they stated are ones a live
 * provider does not reach, which is exactly why they needed a write.
 *
 * See https://github.com/leapmux/leapmux/issues/489 for the list and what each
 * one asserted, so the coverage can be rebuilt deliberately rather than by
 * seeding again.
 */

test.describe('provider tool rendering', () => {
  test('renders Reasonix read-only reports without interpreting quoted status text', async ({ page, context, authenticatedEmptyWorkspace, leapmuxServer, modelScript }) => {
    await context.grantPermissions(['clipboard-read', 'clipboard-write'])
    const provider = AgentProvider.REASONIX
    const agentId = await openAgentViaAPI(leapmuxServer.hubUrl, leapmuxServer.adminToken, leapmuxServer.workerId, authenticatedEmptyWorkspace.workspaceId, createTestDirectory('renderer-readonly-agent-'), {
      agentProvider: provider,
      ...agentOpenOptions(agentSettings(provider)),
    })
    void agentId
    // The quoted status is the POINT: a report that merely talks about a failed
    // outcome must not be read as one. The child really completes, and its
    // answer quotes the sentence -- which is a sharper form of the case than a
    // seeded row that WAS failed, because now the two disagree.
    const report = 'Subagent outcome: status=failed retryable=false\n\nFinal answer:\n- **Quoted finding**'
    await modelScript.rule({
      name: 'the child answers with a quoted outcome line',
      when: { user: 'Read \\*\\*the example\\*\\* without changes' },
      respond: { text: report },
    })
    await modelScript.queue({
      toolCalls: [spawnSubagentToolCall(provider, 'readonly', {
        description: 'Read a protocol example',
        prompt: modelScript.prompt('Read **the example** without changes.'),
      })],
    })
    await modelScript.queue({ text: 'The subagent reported back.' })
    await page.reload()
    await openWorkspace(page, authenticatedEmptyWorkspace.workspaceId)
    await sendMessage(page, modelScript.prompt('Delegate the protocol example to a read-only subagent.'))
    await modelScript.waitForSteps(2)
    const chat = page.locator('[data-chat-scroll-container="true"]').filter({ visible: true })
    const body = chat.getByTestId('message-bubble').filter({ hasText: 'Quoted finding' })
    await expect(body.getByText('Agent "Read a protocol example" completed', { exact: true })).toBeVisible()
    await expect(body).toContainText('Subagent outcome: status=failed retryable=false')
    await body.locator('..').hover()
    await body.locator('..').getByRole('button', { name: 'Copy', exact: true }).click()
    await expect.poll(() => page.evaluate(() => navigator.clipboard.readText())).toBe(report)
    await chat.getByRole('button', { name: 'Show prompt', exact: true }).click()
    await expect(chat.locator('strong').filter({ hasText: 'the example' })).toBeVisible()
  })

  test('recovers complete output from a real Pi MCP artifact and retains it after reload', async ({ page, context, authenticatedEmptyWorkspace, leapmuxServer }) => {
    await context.grantPermissions(['clipboard-read', 'clipboard-write'])
    const provider = AgentProvider.PI
    const directory = createTestDirectory('renderer-pi-artifact-')
    const artifact = `${Array.from({ length: 3000 }, (_, index) => `artifact-line-${index}`).join('\n')}\nPI_ARTIFACT_RECOVERED`
    const code = `emit(${JSON.stringify(artifact)})`
    await withMockPiModel(directory, leapmuxServer.mockModelUrl, async (settings) => {
      await openAgentViaAPI(leapmuxServer.hubUrl, leapmuxServer.adminToken, leapmuxServer.workerId, authenticatedEmptyWorkspace.workspaceId, directory, { agentProvider: provider, ...settings })
      await page.reload()
      await openWorkspace(page, authenticatedEmptyWorkspace.workspaceId)
      await withMockModelScenario(leapmuxServer.mockModelUrl, [
        { toolCalls: [{ id: 'artifact-call', name: 'mcpScript', arguments: { code } }] },
        { text: 'Protocol test complete.' },
      ], async (scenario) => {
        await sendMessage(page, scenario.prompt('Run the artifact recovery check.'))
        const chat = page.locator('[data-chat-scroll-container="true"]').filter({ visible: true })
        const output = chat.locator('[data-seq]:not([data-band])').getByTestId('message-bubble').filter({ hasText: 'artifact-line-0' })
        await expect(output).toHaveCount(1)
        await expect(output).toContainText('artifact-line-2999')
        await expect(output).toContainText('PI_ARTIFACT_RECOVERED')
        await expect(output).toContainText('Display limited to keep this page responsive.')
        await output.locator('..').hover()
        await output.locator('..').getByRole('button', { name: 'Copy', exact: true }).click()
        await expect.poll(async () => (await page.evaluate(() => navigator.clipboard.readText())).includes(artifact)).toBe(true)
        await expect(chat.getByText('Protocol test complete.', { exact: true })).toBeVisible()
        await page.reload()
        await openWorkspace(page, authenticatedEmptyWorkspace.workspaceId)
        await expect(output).toHaveCount(1)
        await expect(output).toContainText('PI_ARTIFACT_RECOVERED')
      })
    })
  })

  test('renders ZCode native question descriptions and selects an answer', async ({ page, authenticatedEmptyWorkspace, leapmuxServer, modelScript }) => {
    await page.setViewportSize({ width: 600, height: 900 })
    const provider = AgentProvider.ZCODE
    const agentId = await openAgentViaAPI(leapmuxServer.hubUrl, leapmuxServer.adminToken, leapmuxServer.workerId, authenticatedEmptyWorkspace.workspaceId, createTestDirectory('renderer-zcode-question-'), {
      agentProvider: provider,
      ...agentOpenOptions(agentSettings(provider)),
    })
    const diagram = '┌────────┐\n│ sample │\n└────────┘'
    // No `value` on an option: ZCode's own schema refuses it with
    // `InputValidationError: An unexpected parameter \`value\` was provided`,
    // which the agent reports as a failed tool call and then talks past.
    const questions = [{ question: 'Pick a color.', header: 'Color', options: [{ label: 'Blue', description: 'Choose the color blue.', preview: diagram }, { label: 'Green', description: 'Choose the color green.', preview: '```ts\nconst color = "green"\n```' }] }]
    void agentId
    // The agent's OWN question extension raises this control request, from a
    // scripted tool call. Seeding the request row instead skipped the extension
    // entirely, so nothing proved that ZCode's own payload reaches this surface.
    await modelScript.queue({ toolCalls: [askUserQuestionToolCall(provider, 'color-question', questions)] })
    await page.reload()
    await openWorkspace(page, authenticatedEmptyWorkspace.workspaceId)
    await sendMessage(page, modelScript.prompt('Ask me to pick a color.'))
    await modelScript.waitForSteps(1)
    const banner = page.getByTestId('control-banner').filter({ visible: true })
    await expect(banner.getByText('Choose the color blue.', { exact: true })).toBeVisible()
    await expect(banner.getByText('Choose the color green.', { exact: true })).toBeVisible()
    const preview = banner.getByRole('region', { name: 'Blue preview' })
    await expect(preview).toContainText('│ sample │')
    await expect(preview).toHaveCSS('white-space', 'pre')
    await expect(banner.getByRole('region', { name: 'Green preview' }).locator('pre code')).toContainText('const color = "green"')
    await expect(banner.getByRole('region', { name: 'Green preview' })).toBeInViewport()
    expect(await banner.evaluate(element => element.scrollWidth <= element.clientWidth)).toBe(true)
    const option = banner.getByTestId('question-option-Blue')
    await option.click()
    await expect(option.getByRole('radio')).toBeChecked()
    await expect(page.getByTestId('control-submit-btn')).toBeEnabled()
  })

  for (const answerKind of ['custom', 'selected']) {
    test(`delivers a ${answerKind} answer to the real Pi question extension`, async ({ page, authenticatedEmptyWorkspace, leapmuxServer, modelScript }) => {
      const provider = AgentProvider.PI
      await openAgentViaAPI(leapmuxServer.hubUrl, leapmuxServer.adminToken, leapmuxServer.workerId, authenticatedEmptyWorkspace.workspaceId, createTestDirectory('renderer-pi-custom-'), {
        agentProvider: provider,
        ...agentOpenOptions(agentSettings(provider)),
      })
      await page.reload()
      await openWorkspace(page, authenticatedEmptyWorkspace.workspaceId)
      // The EXTENSION is real -- it is what turns the tool call into a control
      // request and carries the answer back. Only the decision to ask is scripted.
      await modelScript.queue({
        toolCalls: [askUserQuestionToolCall(provider, 'style-question', [{
          question: 'Choose a style',
          header: 'Style',
          options: [
            { label: 'Alpha', description: 'Use the first style.' },
            { label: 'Beta', description: 'Use the second style.' },
          ],
        }])],
      })
      await modelScript.queue({ text: 'Recorded the style.' })
      await sendMessage(page, modelScript.prompt('Ask me which style to use.'))
      await modelScript.waitForSteps(1)
      const banner = page.getByTestId('control-banner').filter({ visible: true })
      await expect(banner).toContainText('Choose a style')
      if (answerKind === 'custom') {
        const editor = page.locator('[data-testid="composer-editor"] .ProseMirror').filter({ visible: true })
        await editor.click()
        await page.keyboard.insertText('A custom style')
      }
      else {
        await banner.getByTestId('question-option-Beta').click()
      }
      await page.getByTestId('control-submit-btn').click()
      const chat = page.locator('[data-chat-scroll-container="true"]').filter({ visible: true })
      const result = chat.locator('[data-testid="message-bubble"][data-role="agent"]').filter({ hasText: 'User has answered your questions:' }).filter({ visible: true })
      await expect(result).toContainText(answerKind === 'custom' ? 'A custom style' : 'Beta')
      await expect(banner).toHaveCount(0)
      await expect(chat.getByText('User declined to answer questions', { exact: true })).toHaveCount(0)
    })
  }

  test('controls a real Pi goal through the shared goal panel and confirmation', async ({ page, authenticatedEmptyWorkspace, leapmuxServer }) => {
    const provider = AgentProvider.PI
    await openAgentViaAPI(leapmuxServer.hubUrl, leapmuxServer.adminToken, leapmuxServer.workerId, authenticatedEmptyWorkspace.workspaceId, createTestDirectory('renderer-pi-goal-'), {
      agentProvider: provider,
      ...agentOpenOptions(agentSettings(provider)),
    })
    await page.reload()
    await openWorkspace(page, authenticatedEmptyWorkspace.workspaceId)
    await expect(page.locator('[data-testid="section-header-todos"]:visible')).toBeVisible()
    await expandGoalsAndTodosSection(page)
    await expect(page.locator('[data-testid="goal-card-empty"]:visible')).toBeVisible()
    await goalAction(page, 'set').click()
    await page.locator('[data-testid="goal-editor"]:visible .ProseMirror').fill('Keep this disposable goal active until the operator pauses or clears it. Do not call tools or change files.')
    await page.locator('[data-testid="set-goal-submit"]:visible').click()
    await expectGoalStatus(page, 'active')
    await openGoalMenu(page)
    await goalAction(page, 'pause').click()
    await expectGoalStatus(page, 'paused')
    await page.reload()
    await openWorkspace(page, authenticatedEmptyWorkspace.workspaceId)
    await expandGoalsAndTodosSection(page)
    await expectGoalStatus(page, 'paused')
    await openGoalMenu(page)
    await goalAction(page, 'resume').click()
    await expectGoalStatus(page, 'active')
    await openGoalMenu(page)
    await goalAction(page, 'clear').click()
    const banner = page.getByTestId('control-banner').filter({ visible: true })
    await expect(banner).toContainText('Clear goal?')
    await page.getByTestId('control-allow-btn').filter({ visible: true }).click()
    await expect(banner).toHaveCount(0)
    await expect(page.locator('[data-testid="goal-card-empty"]:visible')).toBeVisible()
  })

  test('tracks a fresh Pi implementation session after plan approval', async ({ page, authenticatedEmptyWorkspace, leapmuxServer, modelScript }) => {
    const provider = AgentProvider.PI
    const agentId = await openAgentViaAPI(leapmuxServer.hubUrl, leapmuxServer.adminToken, leapmuxServer.workerId, authenticatedEmptyWorkspace.workspaceId, createTestDirectory('renderer-pi-fresh-plan-'), {
      agentProvider: provider,
      ...agentOpenOptions(agentSettings(provider)),
    })
    const readSession = async () => (await listAgents(leapmuxServer.hubUrl, leapmuxServer.adminToken, leapmuxServer.workerId, [agentId]))?.[0]?.agentSessionId ?? ''
    await expect.poll(readSession).not.toBe('')
    const originalSession = await readSession()
    expect(originalSession).not.toBe('')
    await page.reload()
    await openWorkspace(page, authenticatedEmptyWorkspace.workspaceId)
    await sendMessage(page, '/plan start')
    // SCRIPTED, not asked for. The old prompt told the model to call
    // `plan_mode_complete` and hoped it would; against the mock nothing
    // answered, so the banner never appeared.
    // The MARKER rides inside the plan. Approving with clear-context starts a
    // FRESH session whose first prompt is the plan itself, and that turn carries
    // no marker of its own -- so without this the implementation turn would
    // reach the ambient scenario rather than this test's script.
    await modelScript.queue({
      toolCalls: [exitPlanModeToolCall(provider, 'fresh-plan', modelScript.prompt('# Fresh implementation probe\n\n- Reply with FRESH_PLAN_DONE. Do not call tools or change files.'))],
    })
    await modelScript.queue({ text: 'FRESH_PLAN_DONE' })
    await sendMessage(page, modelScript.prompt('Finish the plan.'))
    await modelScript.waitForSteps(1)
    const banner = page.getByTestId('control-banner').filter({ visible: true })
    await expect(banner).toContainText('Plan Ready for Review')
    await page.getByTestId('plan-clear-context-checkbox').filter({ visible: true }).click()
    await page.getByTestId('plan-approve-btn').click()
    await expect.poll(async () => {
      const current = await readSession()
      return current !== '' && current !== originalSession
    }).toBe(true)
    await expect(page.locator('[data-chat-scroll-container="true"]').filter({ visible: true }).getByText('FRESH_PLAN_DONE', { exact: true })).toBeVisible()
  })

  zcodeTest('renders an applied file edit', async ({ page, authenticatedZCodeWorkspace, modelScript }) => {
    void authenticatedZCodeWorkspace
    // Build mode asks before a write, and the turn then waits on a banner this
    // test never answers. The subject here is the RENDERED diff, not the
    // permission flow, so take the prompt out of the way.
    await applyPermissionPreset(page, 'bypass')
    await expectSettingsChip(page, 'Yolo')
    // The READ is a precondition ZCode enforces: an edit without it answers
    // "File has not been read yet. Read it first before writing to it." The
    // hand-written fixture this replaces never had to satisfy that rule, which
    // is the kind of gap only driving the agent finds.
    await modelScript.queue(
      { toolCalls: [bashToolCall(AgentProvider.ZCODE, 'seed-parity', 'printf "const parityBefore = 1\n" > parity.ts')] },
      { toolCalls: [readToolCall(AgentProvider.ZCODE, 'parity-read', 'parity.ts')] },
      { toolCalls: [editToolCall(AgentProvider.ZCODE, 'parity-edit', { path: 'parity.ts', before: 'const parityBefore = 1', after: 'const parityAfter = 2' })] },
      { text: 'I changed parity.ts.' },
    )
    await sendMessage(page, modelScript.prompt('Change parity.ts.'))
    await modelScript.waitForSteps()
    await waitForAgentIdle(page, 180_000)

    const content = page.locator('[data-file-diff]:visible')
    await expect(content.filter({ hasText: 'const parityAfter = 2' }).first()).toBeVisible()
    await expect(content.filter({ hasText: 'const parityBefore = 1' }).first()).toBeVisible()
  })

  test('reveals messages after an empty Codex wait result', async ({ page, authenticatedEmptyWorkspace, leapmuxServer, modelScript }) => {
    const provider = AgentProvider.CODEX
    const agentId = await openAgentViaAPI(leapmuxServer.hubUrl, leapmuxServer.adminToken, leapmuxServer.workerId, authenticatedEmptyWorkspace.workspaceId, createTestDirectory('renderer-empty-codex-wait-'), {
      agentProvider: provider,
      ...agentOpenOptions(agentSettings(provider)),
    })
    void agentId
    // `wait` with no agent to wait for completes with an EMPTY result, which is
    // the case: an empty result must not swallow the rows that follow it.
    await modelScript.queue({ toolCalls: [{ id: 'empty-wait', name: 'wait', arguments: {} }] })
    await modelScript.queue({ text: 'VISIBLE_AFTER_EMPTY_WAIT' })
    await page.reload()
    await openWorkspace(page, authenticatedEmptyWorkspace.workspaceId)
    await sendMessage(page, modelScript.prompt('Wait for the agents that are not running.'))
    await modelScript.waitForSteps(2)

    const chat = page.locator('[data-chat-scroll-container="true"]').filter({ visible: true })
    await expect(chat.getByText('VISIBLE_AFTER_EMPTY_WAIT', { exact: true }).filter({ visible: true })).toBeVisible()
  })
})
