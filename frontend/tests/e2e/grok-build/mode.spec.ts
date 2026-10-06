import { existsSync, readFileSync } from 'node:fs'
import { join } from 'node:path'
import { expect } from '@playwright/test'
import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { grokTest } from '../grok-fixtures'
import { nativeToolResult } from '../helpers/nativeToolResult'
import { bashToolCall, writeToolCall } from '../helpers/providerToolCalls'
import { applyPermissionPreset, chooseSettingsOption, expectNoControlBanner, expectSettingsChip, expectSettingsOptionChosen, sendMessage, waitForAgentIdle, waitForSettingsHydrated, waitForSettingsIdle } from '../helpers/ui'

grokTest.describe('Grok Build settings, folder trust and MCP forms', () => {
  // Grok reports its session mode and never its approval mode, so the approval
  // presets land on LeapMux's own approval option, and both survive a reload.
  grokTest('sends the effort and session mode into native turns, and keeps the presets after reload', async ({ authenticatedGrokWorkspace, page, modelScript }) => {
    const { workingDir } = authenticatedGrokWorkspace
    await waitForSettingsHydrated(page)
    await expectSettingsChip(page, 'Default')

    // The effort axis is Grok's own `reasoning_effort`, which the plugin declares
    // as its effort group, so the status bar draws it as the effort chip. The
    // approval mode is LeapMux's `approvalMode`, which the status bar does not
    // draw, so the menu states it.
    await chooseSettingsOption(page, 'reasoning_effort-high')
    await waitForSettingsIdle(page)
    await expectSettingsOptionChosen(page, 'reasoning_effort-high')
    await expectSettingsChip(page, /^high$/i)

    const effortStep = await modelScript.queue({ text: 'The high effort turn ended.' })
    await sendMessage(page, modelScript.prompt('Answer once at high effort.'))
    await modelScript.waitForSteps(effortStep + 1)
    await waitForAgentIdle(page)
    expect((await modelScript.requestAt(effortStep)).body).toHaveProperty('reasoning_effort', 'high')

    await chooseSettingsOption(page, 'permissionMode-plan')
    await waitForSettingsIdle(page)
    await expectSettingsChip(page, 'Plan')

    const written = join(workingDir, 'plan-denied-proof.txt')
    const planStep = await modelScript.queue(
      { toolCalls: [writeToolCall(AgentProvider.GROK_BUILD, 'plan-write-proof', { path: written, content: 'GROK_PLAN_WRITE_42\n' })] },
      { text: 'The Plan turn ended.' },
    )
    await sendMessage(page, modelScript.prompt('Try the scripted file write in Plan mode.'))
    await modelScript.waitForSteps(planStep + 2)
    await waitForAgentIdle(page)
    expect(existsSync(written)).toBe(false)
    const planRequest = await modelScript.requestAt(planStep + 1)
    const planMessages = (planRequest.body as { messages?: { role?: string, content?: unknown }[] } | undefined)?.messages ?? []
    const planToolResults = planMessages.filter(message => message.role === 'tool')
    expect(JSON.stringify(planToolResults)).toMatch(/plan|refus|denied|not allowed/i)

    await page.reload()
    await waitForSettingsHydrated(page)
    await expectSettingsChip(page, 'Plan')
    await expectSettingsOptionChosen(page, 'reasoning_effort-high')
    await chooseSettingsOption(page, 'permissionMode-default')
    await waitForSettingsIdle(page)
    await expectSettingsChip(page, 'Default')

    await applyPermissionPreset(page, 'smart')
    await expectSettingsOptionChosen(page, 'approvalMode-auto')

    const autoProof = join(workingDir, 'grok-auto-proof.txt')
    const touchStep = await modelScript.queue(
      { toolCalls: [bashToolCall(AgentProvider.GROK_BUILD, 'smart-touch', 'touch grok-auto-proof.txt')] },
      { text: 'The Smart command ended.' },
    )
    await sendMessage(page, modelScript.prompt('Try the scripted command under Smart permissions.'))
    await modelScript.waitForSteps(touchStep + 2)
    await waitForAgentIdle(page)
    await expectNoControlBanner(page)
    expect(existsSync(autoProof)).toBe(true)

    await modelScript.rule({
      name: 'grok-smart-removal',
      when: { system: '^You review a command that a coding agent wants to run' },
      respond: { text: JSON.stringify({ thinking: 'The command removes a file.', shouldBlock: true, reason: 'Ask the user before removal.' }) },
      once: true,
    })
    const removeProof = 'rm -rf grok-auto-proof.txt'
    const smartStep = await modelScript.queue(
      { toolCalls: [bashToolCall(AgentProvider.GROK_BUILD, 'smart-remove', removeProof)] },
      { text: 'The Smart removal was blocked.' },
    )
    await sendMessage(page, modelScript.prompt('Try the scripted removal under Smart permissions.'))
    await modelScript.waitForSteps(smartStep + 2)
    await waitForAgentIdle(page)
    await expectNoControlBanner(page)
    const smartRequest = await modelScript.requestAt(smartStep + 1)
    const smartMessages = (smartRequest.body as { messages?: { role?: string, content?: unknown, tool_call_id?: string }[] } | undefined)?.messages ?? []
    expect(smartMessages.find(message => message.tool_call_id === 'smart-remove')?.content).toContain('Auto mode blocked this action')
    expect(existsSync(autoProof)).toBe(true)
    expect((await modelScript.status()).ruleMatches['grok-smart-removal']).toBe(1)

    await applyPermissionPreset(page, 'bypass')
    await expectSettingsOptionChosen(page, 'approvalMode-always-approve')

    const bypassStep = await modelScript.queue(
      { toolCalls: [bashToolCall(AgentProvider.GROK_BUILD, 'bypass-remove', removeProof)] },
      { text: 'The Bypass removal ended.' },
    )
    await sendMessage(page, modelScript.prompt('Run the scripted removal under Bypass permissions.'))
    const bypassStatus = await modelScript.waitForSteps(bypassStep + 2)
    await expectNoControlBanner(page)
    await waitForAgentIdle(page)
    expect(existsSync(autoProof)).toBe(false)
    expect(bypassStatus.ruleMatches['grok-smart-removal']).toBe(1)

    await page.reload()
    await waitForSettingsHydrated(page)
    await expectSettingsChip(page, 'Default')
    await expectSettingsOptionChosen(page, 'reasoning_effort-high')
    await expectSettingsOptionChosen(page, 'approvalMode-always-approve')

    const restoredStep = await modelScript.queue(
      { toolCalls: [bashToolCall(AgentProvider.GROK_BUILD, 'restored-calculated-write', 'echo "GROK_RESTORED_$((40 + 2))" > grok-restored-proof.txt; cat grok-restored-proof.txt')] },
      { text: 'The restored native command ended.' },
    )
    await sendMessage(page, modelScript.prompt('Run the calculated write under the restored permissions.'))
    await modelScript.waitForSteps(restoredStep + 2)
    await waitForAgentIdle(page)
    await expectNoControlBanner(page)
    expect(readFileSync(join(workingDir, 'grok-restored-proof.txt'), 'utf8')).toBe('GROK_RESTORED_42\n')
    const restored = await modelScript.requestAt(restoredStep + 1)
    expect(nativeToolResult(restored, 'restored-calculated-write')).toContain('GROK_RESTORED_42')
    expect(restored.body).toHaveProperty('reasoning_effort', 'high')

    await chooseSettingsOption(page, 'approvalMode-ask')
    await waitForSettingsIdle(page)
    await expectSettingsChip(page, 'Default')
    await expectSettingsOptionChosen(page, 'approvalMode-ask')
  })
})
