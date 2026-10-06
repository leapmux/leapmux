import { existsSync, readFileSync } from 'node:fs'
import { join } from 'node:path'
import { expect } from '@playwright/test'
import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { exerciseNativePermissionRefusal } from '../helpers/nativePermission'
import { nativeToolResultAt } from '../helpers/nativeToolExecution'
import { writeToolCall } from '../helpers/providerToolCalls'
import { applyPermissionPreset, chooseSettingsOption, expectNoControlBanner, expectNoSettingsChip, expectSettingsChip, expectSettingsOptionChosen, sendMessage, waitForAgentIdle, waitForSettingsHydrated, waitForSettingsIdle } from '../helpers/ui'
import { qwenTest } from '../qwen-fixtures'
import { expectQwenCanceledTool, qwenClassifierWithoutVerdict } from './autoClassifier'

const CLASSIFIER_RULE = 'qwen-mode-classifier-states-no-verdict'

qwenTest.describe('Qwen Code settings and goal', () => {
  // Qwen's approval modes ARE its session modes, so the two presets land on the
  // permission-mode axis, and each choice survives a reload.
  qwenTest('sends the effort and Auto Edit mode into native turns, and keeps the presets after reload', async ({ native, authenticatedQwenWorkspace }) => {
    const { page, modelScript } = native
    const { workingDir } = authenticatedQwenWorkspace
    await waitForSettingsHydrated(page)
    await expectSettingsChip(page, 'Default')

    // Qwen's effort axis is its own `reasoning_effort`. The plugin declares it
    // as its effort group, so the status bar draws it as the effort chip.
    await chooseSettingsOption(page, 'reasoning_effort-low')
    await waitForSettingsIdle(page)
    await expectSettingsOptionChosen(page, 'reasoning_effort-low')
    await expectSettingsChip(page, /^low$/i)

    const effortStep = await modelScript.queue({ text: 'The low effort turn ended.' })
    await sendMessage(page, modelScript.prompt('Answer once with low effort.'))
    await modelScript.waitForSteps(effortStep + 1)
    await waitForAgentIdle(page)
    expect((await modelScript.requestAt(effortStep)).body).toHaveProperty('reasoning_effort', 'low')

    await chooseSettingsOption(page, 'permissionMode-auto-edit')
    await waitForSettingsIdle(page)
    await expectSettingsChip(page, 'Auto Edit')

    // Auto Edit writes the file without a banner, so the scenario answers no control.
    const written = join(workingDir, 'auto-edit-proof.txt')
    const autoEditStep = await modelScript.queue(
      { toolCalls: [writeToolCall(AgentProvider.QWEN_CODE, 'auto-edit-proof', { path: written, content: 'QWEN_AUTO_EDIT_42\n' })] },
      { text: 'The Auto Edit turn ended.' },
    )
    await sendMessage(page, modelScript.prompt('Create the scripted file in Auto Edit mode.'))
    await modelScript.waitForSteps(autoEditStep + 1)
    await expectNoControlBanner(page)
    await modelScript.waitForSteps(autoEditStep + 2)
    await waitForAgentIdle(page)
    expect(existsSync(written)).toBe(true)
    expect(readFileSync(written, 'utf8')).toBe('QWEN_AUTO_EDIT_42\n')

    await applyPermissionPreset(page, 'smart')
    await expectSettingsChip(page, /^Auto$/)

    // Auto mode asks its classifier before it runs this write. A classifier that states no verdict makes Qwen ask the
    // reader, and a reject ends the turn with no further model request.
    await modelScript.rule(qwenClassifierWithoutVerdict(CLASSIFIER_RULE))
    const protectedFile = join(workingDir, 'package.json')
    const protectedWrite = writeToolCall(AgentProvider.QWEN_CODE, 'smart-protected-write', { path: protectedFile, content: '{"private":true}\n' })
    await exerciseNativePermissionRefusal(native, {
      toolCall: protectedWrite,
      prompt: 'Try the scripted protected write under Smart permissions.',
      bannerText: 'package.json',
      expectUnchanged: () => expect(existsSync(protectedFile)).toBe(false),
      nativeRefusal: snapshot => expectQwenCanceledTool(snapshot, protectedWrite),
    })
    expect((await modelScript.status()).ruleMatches[CLASSIFIER_RULE], 'Auto mode asked its classifier before it asked the reader').toBeGreaterThan(0)
    // The refusal scenario reloads the page.
    await waitForSettingsHydrated(page)

    await applyPermissionPreset(page, 'bypass')
    await expectSettingsChip(page, 'YOLO')

    const bypassStep = await modelScript.queue(
      { toolCalls: [writeToolCall(AgentProvider.QWEN_CODE, 'bypass-protected-write', { path: protectedFile, content: '{"proof":"QWEN_BYPASS_42"}\n' })] },
      { text: 'The protected write ran in YOLO.' },
    )
    await sendMessage(page, modelScript.prompt('Run the scripted protected write under Bypass permissions.'))
    await modelScript.waitForSteps(bypassStep + 1)
    await expectNoControlBanner(page)
    await modelScript.waitForSteps(bypassStep + 2)
    await waitForAgentIdle(page)
    expect(readFileSync(protectedFile, 'utf8')).toBe('{"proof":"QWEN_BYPASS_42"}\n')

    await page.reload()
    await waitForSettingsHydrated(page)
    await expectSettingsChip(page, 'YOLO')
    await expectSettingsOptionChosen(page, 'reasoning_effort-low')

    const restoredStep = await modelScript.queue(
      { toolCalls: [writeToolCall(AgentProvider.QWEN_CODE, 'restored-protected-write', { path: protectedFile, content: '{"proof":"QWEN_RESTORED_43"}\n' })] },
      { text: 'The restored protected write ran in YOLO.' },
    )
    await sendMessage(page, modelScript.prompt('Write the new protected file value under the restored permissions.'))
    await modelScript.waitForSteps(restoredStep + 2)
    await waitForAgentIdle(page)
    await expectNoControlBanner(page)
    expect(readFileSync(protectedFile, 'utf8')).toBe('{"proof":"QWEN_RESTORED_43"}\n')
    expect(await nativeToolResultAt(modelScript, restoredStep + 1, 'restored-protected-write')).toMatch(/success|wrote|written/i)
    expect((await modelScript.requestAt(restoredStep + 1)).body).toHaveProperty('reasoning_effort', 'low')

    await chooseSettingsOption(page, 'permissionMode-default')
    await waitForSettingsIdle(page)
    await expectSettingsChip(page, 'Default')
    await expectNoSettingsChip(page, 'YOLO')
  })
})
