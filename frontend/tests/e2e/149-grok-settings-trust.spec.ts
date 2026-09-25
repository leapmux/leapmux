import { existsSync, writeFileSync } from 'node:fs'
import { join } from 'node:path'
import process from 'node:process'
import { AgentProvider } from '../../src/generated/proto/leapmux/v1/agent_pb'
import { agentOpenOptions, agentSettings } from './agentSettings'
import { expect, GROK_E2E_SKIP_REASON, grokTest, openGrokAgent } from './grok-fixtures'
import { openAgentViaAPI } from './helpers/api'
import { mcpToolCall } from './helpers/providerToolCalls'
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
  // Grok reports its session mode and never its approval mode, so the approval
  // presets land on LeapMux's own approval option, and both survive a reload.
  grokTest('switches the effort, the session mode and the approval presets, and keeps them after reload', async ({ page, authenticatedEmptyWorkspace, leapmuxServer }) => {
    await openGrokAgent(leapmuxServer, authenticatedEmptyWorkspace.workspaceId)
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

    await chooseSettingsOption(page, 'permissionMode-plan')
    await waitForSettingsIdle(page)
    await expectSettingsChip(page, 'Plan')

    await applyPermissionPreset(page, 'smart')
    await expectSettingsOptionChosen(page, 'approvalMode-auto')
    await applyPermissionPreset(page, 'bypass')
    await expectSettingsOptionChosen(page, 'approvalMode-always-approve')

    await page.reload()
    await waitForSettingsHydrated(page)
    await expectSettingsChip(page, 'Plan')
    await expectSettingsOptionChosen(page, 'reasoning_effort-high')
    await expectSettingsOptionChosen(page, 'approvalMode-always-approve')

    await chooseSettingsOption(page, 'permissionMode-default')
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
    await waitForAgentIdle(page, 120_000)
    expect(JSON.stringify((await modelScript.status()).requests.at(-1)?.body)).toContain('FORM_ROUND_TRIP_OK')
    await expect(assistantBubbles(page).filter({ hasText: 'The form came back.' })).toBeVisible()
  })
})
