import { existsSync, readFileSync } from 'node:fs'
import { join } from 'node:path'
import { expect } from '@playwright/test'
import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { GROK_AGENT, grokTest } from '../grok-fixtures'
import { nativeToolResult } from '../helpers/nativeToolResult'
import { bashToolCall, writeToolCall } from '../helpers/providerToolCalls'
import { applyPermissionPreset, chooseSettingsOption, expectSettingsChip, expectSettingsOptionChosen, openWorkspace, sendMessage, waitForAgentIdle, waitForSettingsHydrated, waitForSettingsIdle } from '../helpers/ui'
import { openProviderAgent } from '../helpers/workspace'

grokTest.describe('Grok Build settings, folder trust and MCP forms', () => {
  // Grok reports its session mode and never its approval mode, so the approval
  // presets land on LeapMux's own approval option, and both survive a reload.
  grokTest('sends the effort and session mode into native turns, and keeps the presets after reload', async ({ page, authenticatedEmptyWorkspace, leapmuxServer, modelScript }) => {
    const { workingDir } = await openProviderAgent(leapmuxServer, authenticatedEmptyWorkspace.workspaceId, GROK_AGENT)
    await openWorkspace(page, authenticatedEmptyWorkspace.workspaceId)
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

    await modelScript.queue({ text: 'The high effort turn ended.' })
    await sendMessage(page, modelScript.prompt('Answer once at high effort.'))
    const effortStatus = await modelScript.waitForSteps()
    await waitForAgentIdle(page)
    const effortRequest = effortStatus.requests.find(request => request.stepIndex === 0)
    expect((effortRequest?.body as { reasoning_effort?: unknown } | undefined)?.reasoning_effort).toBe('high')

    await chooseSettingsOption(page, 'permissionMode-plan')
    await waitForSettingsIdle(page)
    await expectSettingsChip(page, 'Plan')

    const written = join(workingDir, 'plan-denied-proof.txt')
    await modelScript.queue(
      { toolCalls: [writeToolCall(AgentProvider.GROK_BUILD, 'plan-write-proof', { path: written, content: 'GROK_PLAN_WRITE_42\n' })] },
      { text: 'The Plan turn ended.' },
    )
    await sendMessage(page, modelScript.prompt('Try the scripted file write in Plan mode.'))
    await modelScript.waitForSteps()
    await waitForAgentIdle(page)
    expect(existsSync(written)).toBe(false)
    const planRequest = (await modelScript.status()).requests.find(request => request.stepIndex === 2)
    const planMessages = (planRequest?.body as { messages?: { role?: string, content?: unknown }[] } | undefined)?.messages ?? []
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
    await modelScript.queue(
      { toolCalls: [bashToolCall(AgentProvider.GROK_BUILD, 'smart-touch', 'touch grok-auto-proof.txt')] },
      { text: 'The Smart command ended.' },
    )
    await sendMessage(page, modelScript.prompt('Try the scripted command under Smart permissions.'))
    await modelScript.waitForSteps()
    await waitForAgentIdle(page)
    const banner = page.getByTestId('control-banner').filter({ visible: true })
    await expect(banner).toHaveCount(0)
    expect(existsSync(autoProof)).toBe(true)

    await modelScript.rule({
      name: 'grok-smart-removal',
      when: { system: '^You review a command that a coding agent wants to run' },
      respond: { text: JSON.stringify({ thinking: 'The command removes a file.', shouldBlock: true, reason: 'Ask the user before removal.' }) },
      once: true,
    })
    const removeProof = 'rm -rf grok-auto-proof.txt'
    await modelScript.queue(
      { toolCalls: [bashToolCall(AgentProvider.GROK_BUILD, 'smart-remove', removeProof)] },
      { text: 'The Smart removal was blocked.' },
    )
    await sendMessage(page, modelScript.prompt('Try the scripted removal under Smart permissions.'))
    const smartStatus = await modelScript.waitForSteps()
    await waitForAgentIdle(page)
    await expect(banner).toHaveCount(0)
    const smartRequest = smartStatus.requests.find(request => request.stepIndex === 6)
    const smartMessages = (smartRequest?.body as { messages?: { role?: string, content?: unknown, tool_call_id?: string }[] } | undefined)?.messages ?? []
    expect(smartMessages.find(message => message.tool_call_id === 'smart-remove')?.content).toContain('Auto mode blocked this action')
    expect(existsSync(autoProof)).toBe(true)
    expect((await modelScript.status()).ruleMatches['grok-smart-removal']).toBe(1)

    await applyPermissionPreset(page, 'bypass')
    await expectSettingsOptionChosen(page, 'approvalMode-always-approve')

    await modelScript.queue(
      { toolCalls: [bashToolCall(AgentProvider.GROK_BUILD, 'bypass-remove', removeProof)] },
      { text: 'The Bypass removal ended.' },
    )
    await sendMessage(page, modelScript.prompt('Run the scripted removal under Bypass permissions.'))
    const bypassStatus = await modelScript.waitForSteps()
    await expect(banner).toHaveCount(0)
    await waitForAgentIdle(page)
    expect(existsSync(autoProof)).toBe(false)
    expect(bypassStatus.ruleMatches['grok-smart-removal']).toBe(1)

    await page.reload()
    await waitForSettingsHydrated(page)
    await expectSettingsChip(page, 'Default')
    await expectSettingsOptionChosen(page, 'reasoning_effort-high')
    await expectSettingsOptionChosen(page, 'approvalMode-always-approve')

    await modelScript.queue(
      { toolCalls: [bashToolCall(AgentProvider.GROK_BUILD, 'restored-calculated-write', 'echo "GROK_RESTORED_$((40 + 2))" > grok-restored-proof.txt; cat grok-restored-proof.txt')] },
      { text: 'The restored native command ended.' },
    )
    await sendMessage(page, modelScript.prompt('Run the calculated write under the restored permissions.'))
    const restoredStatus = await modelScript.waitForSteps()
    await waitForAgentIdle(page)
    await expect(banner).toHaveCount(0)
    expect(readFileSync(join(workingDir, 'grok-restored-proof.txt'), 'utf8')).toBe('GROK_RESTORED_42\n')
    const restored = restoredStatus.requests.find(request => request.stepIndex === 10)
    if (!restored)
      throw new Error('The restored Grok command produced no native result.')
    expect(nativeToolResult(restored, 'restored-calculated-write')).toContain('GROK_RESTORED_42')
    expect(restored.body).toHaveProperty('reasoning_effort', 'high')

    await chooseSettingsOption(page, 'approvalMode-ask')
    await waitForSettingsIdle(page)
    await expectSettingsChip(page, 'Default')
    await expectSettingsOptionChosen(page, 'approvalMode-ask')
  })
})
