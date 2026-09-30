import { existsSync, writeFileSync } from 'node:fs'
import { join } from 'node:path'
import process from 'node:process'
import { AgentProvider } from '../../src/generated/proto/leapmux/v1/agent_pb'
import { agentOpenOptions, agentSettings } from './agentSettings'
import { expect, GROK_E2E_SKIP_REASON, grokTest, openGrokAgent } from './grok-fixtures'
import { openAgentViaAPI } from './helpers/api'
import { GROK_ALT_MODEL_ID } from './helpers/mockAgentEnvironment'
import { exerciseProviderSteer } from './helpers/providerSteer'
import { bashToolCall, mcpToolCall, writeToolCall } from './helpers/providerToolCalls'
import { createTestDirectory } from './helpers/runDirectory'
import { applyPermissionPreset, assistantBubbles, chooseSettingsOption, expectSettingsChip, expectSettingsOptionChosen, messageBubbles, openWorkspace, sendMessage, waitForAgentIdle, waitForSettingsHydrated, waitForSettingsIdle } from './helpers/ui'
import { createGitRepo } from './helpers/worktree'

grokTest.skip(!!GROK_E2E_SKIP_REASON, GROK_E2E_SKIP_REASON || '')

/**
 * A disposable MCP server whose one tool asks for a form.
 *
 * It writes `ready` beside itself when Grok lists its tools, which is the moment
 * the tool exists for the model, and it answers the call with whether the form
 * came back with the value the test typed.
 */
const FORM_SERVER = `
import { createInterface } from 'node:readline';
import { writeFileSync } from 'node:fs';
import { dirname, join } from 'node:path';
import { fileURLToPath } from 'node:url';
const ready = join(dirname(fileURLToPath(import.meta.url)), 'ready');
let toolRequest;
const send = value => process.stdout.write(JSON.stringify(value) + '\\n');
for await (const line of createInterface({ input: process.stdin })) {
  const request = JSON.parse(line);
  if (request.id === undefined) continue;
  if (request.id === 'probe-form' && !request.method) {
    const reply = request.result;
    const valid = reply?.action === 'accept' && reply.content?.name === 'grok-e2e';
    send({jsonrpc:'2.0',id:toolRequest,result:{content:[{type:'text',text:valid ? 'FORM_ROUND_TRIP_OK' : 'FORM_ROUND_TRIP_FAILED'}]}});
    continue;
  }
  let result;
  switch (request.method) {
    case 'initialize':
      result = {protocolVersion:request.params.protocolVersion,capabilities:{tools:{}},serverInfo:{name:'form_probe',version:'1'}};
      break;
    case 'tools/list':
      writeFileSync(ready, '');
      result = {tools:[{name:'ask',description:'Request the disposable probe form. Call once with no arguments.',inputSchema:{type:'object',properties:{}}}]};
      break;
    case 'tools/call':
      toolRequest = request.id;
      send({jsonrpc:'2.0',id:'probe-form',method:'elicitation/create',params:{mode:'form',message:'Name the probe.',requestedSchema:{type:'object',required:['name'],properties:{name:{type:'string',title:'Name'}}}}});
      continue;
    default:
      send({jsonrpc:'2.0',id:request.id,error:{code:-32601,message:'Method not supported'}});
      continue;
  }
  send({jsonrpc:'2.0',id:request.id,result});
}
`

grokTest.describe('Grok Build settings, folder trust and MCP forms', () => {
  grokTest('switches the model for the next native request', async ({ authenticatedGrokWorkspace, page, modelScript }) => {
    void authenticatedGrokWorkspace
    await waitForSettingsHydrated(page)
    await chooseSettingsOption(page, `model-${GROK_ALT_MODEL_ID}`)
    await waitForSettingsIdle(page)
    await expectSettingsOptionChosen(page, `model-${GROK_ALT_MODEL_ID}`)

    await modelScript.queue({ text: 'The alternate model answered.' })
    await sendMessage(page, modelScript.prompt('Reply once with the alternate model.'))
    const status = await modelScript.waitForSteps()
    await waitForAgentIdle(page)
    const body = JSON.stringify(status.requests.find(request => request.stepIndex === 0)?.body)
    expect(body.includes(`"model":"${GROK_ALT_MODEL_ID}"`)).toBe(true)

    await page.reload()
    await expectSettingsOptionChosen(page, `model-${GROK_ALT_MODEL_ID}`)
  })

  grokTest('steers a queued message into the active turn', async ({ page, authenticatedEmptyWorkspace, leapmuxServer, modelScript }) => {
    await openGrokAgent(leapmuxServer, authenticatedEmptyWorkspace.workspaceId, { approvalMode: 'always-approve' })
    await openWorkspace(page, authenticatedEmptyWorkspace.workspaceId)
    await exerciseProviderSteer(page, modelScript, AgentProvider.GROK_BUILD)
  })

  // Grok reports its session mode and never its approval mode, so the approval
  // presets land on LeapMux's own approval option, and both survive a reload.
  grokTest('sends the effort and session mode into native turns, and keeps the presets after reload', async ({ page, authenticatedEmptyWorkspace, leapmuxServer, modelScript }) => {
    const { workingDir } = await openGrokAgent(leapmuxServer, authenticatedEmptyWorkspace.workspaceId)
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

    await chooseSettingsOption(page, 'approvalMode-ask')
    await waitForSettingsIdle(page)
    await expectSettingsChip(page, 'Default')
    await expectSettingsOptionChosen(page, 'approvalMode-ask')
  })

  // A repository that holds its own MCP server is one Grok asks about before it
  // loads anything from it. Trusting it loads the server, and the server's form
  // then round-trips through Grok's own elicitation request.
  grokTest('trusts a repository, loads its MCP server and answers the server\'s form', async ({ page, authenticatedEmptyWorkspace, leapmuxServer, modelScript }) => {
    const repository = createGitRepo(createTestDirectory('grok-trust-'), 'repo')
    const server = join(repository, 'form-server.mjs')
    writeFileSync(server, FORM_SERVER)
    writeFileSync(join(repository, '.mcp.json'), JSON.stringify({ mcpServers: { form_probe: { command: process.execPath, args: [server] } } }))
    const settings = agentOpenOptions(agentSettings(AgentProvider.GROK_BUILD))
    await openAgentViaAPI(leapmuxServer.hubUrl, leapmuxServer.adminToken, leapmuxServer.workerId, authenticatedEmptyWorkspace.workspaceId, repository, {
      agentProvider: AgentProvider.GROK_BUILD,
      ...settings,
    })
    await openWorkspace(page, authenticatedEmptyWorkspace.workspaceId)

    const banner = page.getByTestId('control-banner').filter({ visible: true })
    await expect(banner).toContainText('Trust the workspace')
    await expect(banner).toContainText('mcp')
    expect(existsSync(join(repository, 'ready'))).toBe(false)
    await page.getByTestId('control-allow-btn').filter({ visible: true }).click()
    await expect(banner).toHaveCount(0)
    await expect(messageBubbles(page).filter({ hasText: 'Trust this workspace' }).first()).toBeVisible()
    // Grok loads the trusted server in place; its tool exists once Grok lists it.
    await expect.poll(() => existsSync(join(repository, 'ready'))).toBe(true)

    await modelScript.queue(
      { toolCalls: [mcpToolCall(AgentProvider.GROK_BUILD, 'grok-mcp', { server: 'form_probe', tool: 'ask', input: {} })] },
      { text: 'The form came back.' },
    )
    await sendMessage(page, modelScript.prompt('Call the probe form tool.'))
    await modelScript.waitForSteps(1)
    // Grok asks before an MCP tool runs in its `ask` mode, so the first card is
    // the tool permission, and the server's form follows only once it is allowed.
    const form = page.getByTestId('elicitation-form').filter({ visible: true })
    await expect(banner).toBeVisible()
    await expect(form).toHaveCount(0)
    await page.getByTestId('control-allow-btn').filter({ visible: true }).click()
    await expect(form).toBeVisible()
    await expect(banner).toContainText('Name the probe.')
    await form.getByLabel('Name *').fill('grok-e2e')
    await page.getByTestId('control-allow-btn').filter({ visible: true }).click()
    await expect(banner).toHaveCount(0)
    await modelScript.waitForSteps()
    await waitForAgentIdle(page)
    expect(JSON.stringify((await modelScript.status()).requests.at(-1)?.body)).toContain('FORM_ROUND_TRIP_OK')
    await expect(assistantBubbles(page).filter({ hasText: 'The form came back.' })).toBeVisible()
  })
})
