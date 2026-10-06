import { existsSync, readFileSync } from 'node:fs'
import { join } from 'node:path'
import { expect } from '@playwright/test'
import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { nativeToolResult } from '../helpers/nativeToolResult'
import { writeToolCall } from '../helpers/providerToolCalls'
import { applyPermissionPreset, chooseSettingsOption, expectNoControlBanner, expectNoSettingsChip, expectSettingsChip, expectSettingsOptionChosen, openWorkspace, sendMessage, waitForAgentIdle, waitForControlBanner, waitForSettingsHydrated, waitForSettingsIdle } from '../helpers/ui'
import { openProviderAgent } from '../helpers/workspace'
import { QWEN_AGENT, qwenTest } from '../qwen-fixtures'

qwenTest.describe('Qwen Code settings and goal', () => {
  // Qwen's approval modes ARE its session modes, so the two presets land on the
  // permission-mode axis, and each choice survives a reload.
  qwenTest('sends the effort and Auto Edit mode into native turns, and keeps the presets after reload', async ({ page, authenticatedEmptyWorkspace, leapmuxServer, modelScript }) => {
    const { workingDir } = await openProviderAgent(leapmuxServer, authenticatedEmptyWorkspace.workspaceId, QWEN_AGENT)
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
    await expectNoControlBanner(page)
    await modelScript.waitForSteps()
    await waitForAgentIdle(page)
    expect(readFileSync(protectedFile, 'utf8')).toBe('{"proof":"QWEN_BYPASS_42"}\n')

    await page.reload()
    await waitForSettingsHydrated(page)
    await expectSettingsChip(page, 'YOLO')
    await expectSettingsOptionChosen(page, 'reasoning_effort-low')

    await modelScript.queue(
      { toolCalls: [writeToolCall(AgentProvider.QWEN_CODE, 'restored-protected-write', { path: protectedFile, content: '{"proof":"QWEN_RESTORED_43"}\n' })] },
      { text: 'The restored protected write ran in YOLO.' },
    )
    await sendMessage(page, modelScript.prompt('Write the new protected file value under the restored permissions.'))
    const restoredStatus = await modelScript.waitForSteps()
    await waitForAgentIdle(page)
    await expectNoControlBanner(page)
    expect(readFileSync(protectedFile, 'utf8')).toBe('{"proof":"QWEN_RESTORED_43"}\n')
    const restored = restoredStatus.requests.find(request => request.stepIndex === 8)
    if (!restored)
      throw new Error('The restored Qwen write produced no native result.')
    expect(nativeToolResult(restored, 'restored-protected-write')).toMatch(/success|wrote|written/i)
    expect(restored.body).toHaveProperty('reasoning_effort', 'low')

    await chooseSettingsOption(page, 'permissionMode-default')
    await waitForSettingsIdle(page)
    await expectSettingsChip(page, 'Default')
    await expectNoSettingsChip(page, 'YOLO')
  })
})
