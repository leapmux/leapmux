import { existsSync, readFileSync } from 'node:fs'
import { join } from 'node:path'
import { AgentProvider } from '../../src/generated/proto/leapmux/v1/agent_pb'
import { QWEN_ALT_MODEL_ID, QWEN_ALT_MODEL_WIRE_ID } from './helpers/mockAgentEnvironment'
import { exerciseProviderSteer } from './helpers/providerSteer'
import { writeToolCall } from './helpers/providerToolCalls'
import { expandGoalsAndTodosSection, expectGoalStatus, goalAction, openGoalMenu } from './helpers/subagentRegistry'
import { applyPermissionPreset, chooseSettingsOption, expectNoSettingsChip, expectSettingsChip, expectSettingsOptionChosen, openWorkspace, sendMessage, waitForAgentIdle, waitForControlBanner, waitForSettingsHydrated, waitForSettingsIdle } from './helpers/ui'
import { expect, openQwenAgent, QWEN_E2E_SKIP_REASON, qwenTest } from './qwen-fixtures'

qwenTest.skip(!!QWEN_E2E_SKIP_REASON, QWEN_E2E_SKIP_REASON || '')

qwenTest.describe('Qwen Code settings and goal', () => {
  qwenTest('switches the model for the next native request', async ({ page, authenticatedEmptyWorkspace, leapmuxServer, modelScript }) => {
    await openQwenAgent(leapmuxServer, authenticatedEmptyWorkspace.workspaceId)
    await openWorkspace(page, authenticatedEmptyWorkspace.workspaceId)
    await waitForSettingsHydrated(page)
    await chooseSettingsOption(page, `model-${QWEN_ALT_MODEL_ID}`)
    await waitForSettingsIdle(page)
    await expectSettingsOptionChosen(page, `model-${QWEN_ALT_MODEL_ID}`)

    await modelScript.queue({ text: 'The alternate model answered.' })
    await sendMessage(page, modelScript.prompt('Reply once with the alternate model.'))
    const status = await modelScript.waitForSteps()
    await waitForAgentIdle(page)
    const body = JSON.stringify(status.requests.find(request => request.stepIndex === 0)?.body)
    expect(body.includes(`"model":"${QWEN_ALT_MODEL_WIRE_ID}"`)).toBe(true)

    await page.reload()
    await expectSettingsOptionChosen(page, `model-${QWEN_ALT_MODEL_ID}`)
  })

  qwenTest('steers a queued message into the active turn', async ({ page, authenticatedEmptyWorkspace, leapmuxServer, modelScript }) => {
    await openQwenAgent(leapmuxServer, authenticatedEmptyWorkspace.workspaceId, { permissionMode: 'yolo' })
    await openWorkspace(page, authenticatedEmptyWorkspace.workspaceId)
    await exerciseProviderSteer(page, modelScript, AgentProvider.QWEN_CODE)
  })

  // Qwen's approval modes ARE its session modes, so the two presets land on the
  // permission-mode axis, and each choice survives a reload.
  qwenTest('sends the effort and Auto Edit mode into native turns, and keeps the presets after reload', async ({ page, authenticatedEmptyWorkspace, leapmuxServer, modelScript }) => {
    const { workingDir } = await openQwenAgent(leapmuxServer, authenticatedEmptyWorkspace.workspaceId)
    await openWorkspace(page, authenticatedEmptyWorkspace.workspaceId)
    await waitForSettingsHydrated(page)
    await expectSettingsChip(page, 'Default')

    // Qwen's effort axis is its own `reasoning_effort`. The plugin declares it
    // as its effort group, so the status bar draws it as the effort chip.
    await chooseSettingsOption(page, 'reasoning_effort-low')
    await waitForSettingsIdle(page)
    await expectSettingsOptionChosen(page, 'reasoning_effort-low')
    await expectSettingsChip(page, /^low$/i)

    await modelScript.queue({ text: 'The low effort turn ended.' })
    await sendMessage(page, modelScript.prompt('Answer once with low effort.'))
    const effortStatus = await modelScript.waitForSteps()
    await waitForAgentIdle(page)
    const effortRequest = effortStatus.requests.find(request => request.stepIndex === 0)
    expect((effortRequest?.body as { reasoning_effort?: unknown } | undefined)?.reasoning_effort).toBe('low')

    await chooseSettingsOption(page, 'permissionMode-auto-edit')
    await waitForSettingsIdle(page)
    await expectSettingsChip(page, 'Auto Edit')

    const written = join(workingDir, 'auto-edit-proof.txt')
    await modelScript.queue(
      { toolCalls: [writeToolCall(AgentProvider.QWEN_CODE, 'auto-edit-proof', { path: written, content: 'QWEN_AUTO_EDIT_42\n' })] },
      { text: 'The Auto Edit turn ended.' },
    )
    await sendMessage(page, modelScript.prompt('Create the scripted file in Auto Edit mode.'))
    await modelScript.waitForSteps(2)
    await expect(page.getByTestId('control-banner').filter({ visible: true })).toHaveCount(0)
    await modelScript.waitForSteps()
    await waitForAgentIdle(page)
    expect(existsSync(written)).toBe(true)
    expect(readFileSync(written, 'utf8')).toBe('QWEN_AUTO_EDIT_42\n')

    await applyPermissionPreset(page, 'smart')
    await expectSettingsChip(page, /^Auto$/)

    // Qwen's Auto mode requires review for a protected workspace file.
    const protectedFile = join(workingDir, 'package.json')
    expect(existsSync(protectedFile)).toBe(false)
    await modelScript.queue(
      { toolCalls: [writeToolCall(AgentProvider.QWEN_CODE, 'smart-protected-write', { path: protectedFile, content: '{"private":true}\n' })] },
      { text: 'The protected write was denied.' },
    )
    await sendMessage(page, modelScript.prompt('Try the scripted protected write under Smart permissions.'))
    await modelScript.waitForSteps(4)
    const banner = await waitForControlBanner(page)
    await expect(banner).toContainText('package.json')
    await page.getByTestId('control-deny-btn').filter({ visible: true }).click()
    await modelScript.waitForSteps()
    await waitForAgentIdle(page)
    expect(existsSync(protectedFile)).toBe(false)

    await applyPermissionPreset(page, 'bypass')
    await expectSettingsChip(page, 'YOLO')

    await modelScript.queue(
      { toolCalls: [writeToolCall(AgentProvider.QWEN_CODE, 'bypass-protected-write', { path: protectedFile, content: '{"proof":"QWEN_BYPASS_42"}\n' })] },
      { text: 'The protected write ran in YOLO.' },
    )
    await sendMessage(page, modelScript.prompt('Run the scripted protected write under Bypass permissions.'))
    await modelScript.waitForSteps(6)
    await expect(banner).toHaveCount(0)
    await modelScript.waitForSteps()
    await waitForAgentIdle(page)
    expect(readFileSync(protectedFile, 'utf8')).toBe('{"proof":"QWEN_BYPASS_42"}\n')

    await page.reload()
    await waitForSettingsHydrated(page)
    await expectSettingsChip(page, 'YOLO')
    await expectSettingsOptionChosen(page, 'reasoning_effort-low')

    await chooseSettingsOption(page, 'permissionMode-default')
    await waitForSettingsIdle(page)
    await expectSettingsChip(page, 'Default')
    await expectNoSettingsChip(page, 'YOLO')
  })

  // The goal card drives Qwen's own `/goal` command. Qwen runs the goal turns
  // itself; each one reaches this script through the objective, which carries
  // the marker, and a turn that records no progress counts toward the pause.
  qwenTest('sets, follows and clears a native goal', async ({ page, authenticatedEmptyWorkspace, leapmuxServer, modelScript }) => {
    await openQwenAgent(leapmuxServer, authenticatedEmptyWorkspace.workspaceId)
    await openWorkspace(page, authenticatedEmptyWorkspace.workspaceId)
    await waitForSettingsHydrated(page)
    await modelScript.rule({
      name: 'every goal turn answers DONE',
      when: { body: 'Reply with the word DONE' },
      respond: { text: 'DONE' },
    })

    await expandGoalsAndTodosSection(page)
    await goalAction(page, 'set').click()
    const objective = modelScript.prompt('Reply with the word DONE.')
    await page.locator('[data-testid="goal-editor"]:visible .ProseMirror').fill(objective)
    await page.locator('[data-testid="set-goal-submit"]:visible').click()
    await expect(page.locator('[data-testid="goal-objective"]:visible')).toContainText('Reply with the word DONE.')

    // Qwen pauses a goal after turns that record no progress, and it states why.
    await expectGoalStatus(page, 'paused')
    // A turn Qwen started by itself ends with its own notification, which draws
    // the same divider as a turn the reader started. The `/goal` prompt that set
    // the goal draws the first divider, so a second one proves that a goal round
    // drew its own.
    await expect(page.locator('[data-testid="result-divider"]:visible').nth(1)).toBeVisible()

    const roundsBeforeResume = (await modelScript.status()).ruleMatches['every goal turn answers DONE'] ?? 0
    await openGoalMenu(page)
    await goalAction(page, 'resume').click()
    await expect.poll(async () => (await modelScript.status()).ruleMatches['every goal turn answers DONE'] ?? 0).toBeGreaterThan(roundsBeforeResume)
    await expectGoalStatus(page, 'paused')

    await openGoalMenu(page)
    await goalAction(page, 'clear').click()
    await expect(page.locator('[data-testid="goal-card-empty"]:visible')).toBeVisible()
  })
})
