import { existsSync, mkdirSync, writeFileSync } from 'node:fs'

import { join } from 'node:path'
import process from 'node:process'
import { expect } from '@playwright/test'
import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { mcpToolCall } from '../helpers/providerToolCalls'
import { assistantBubbles, openWorkspace, sendMessage, visibleControlBanner, waitForAgentIdle } from '../helpers/ui'

import { KIRO_E2E_SKIP_REASON, kiroTest, openKiroAgent } from '../kiro-fixtures'

kiroTest.skip(!!KIRO_E2E_SKIP_REASON, KIRO_E2E_SKIP_REASON || '')

const PROVIDER = AgentProvider.KIRO

/**
 * A private MCP server exposes one tool that requests a form.
 * It writes `ready` when Kiro lists the tool, so the test can prove discovery.
 * The tool result states whether the native form reply contains the exact value that the reader entered.
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
    const valid = reply?.action === 'accept' && reply.content?.name === 'kiro-e2e';
    send({jsonrpc:'2.0',id:toolRequest,result:{content:[{type:'text',text:valid ? 'FORM_ROUND_TRIP_OK' : 'FORM_ROUND_TRIP_FAILED'}]}});
    continue;
  }
  let result;
  switch (request.method) {
    case 'initialize':
      result = {protocolVersion:request.params.protocolVersion,capabilities:{tools:{}},serverInfo:{name:'probe',version:'1'}};
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

kiroTest.describe('Kiro control requests', () => {
  // A workspace MCP server's form round-trips through Kiro's own elicitation request.
  kiroTest('answers the form that an MCP server asks for', async ({ page, authenticatedEmptyWorkspace, leapmuxServer, modelScript }) => {
    const { workingDir } = await openKiroAgentWithServer(leapmuxServer, authenticatedEmptyWorkspace.workspaceId)
    await openWorkspace(page, authenticatedEmptyWorkspace.workspaceId)
    // Kiro loads the server in the background. Its tool exists once Kiro lists it.
    await expect.poll(() => existsSync(join(workingDir, 'ready'))).toBe(true)

    await modelScript.queue(
      { toolCalls: [mcpToolCall(PROVIDER, 'kiro-mcp', { server: 'probe', tool: 'ask', input: {} })] },
      { text: 'The form came back.' },
    )
    await sendMessage(page, modelScript.prompt('Call the probe form tool.'))
    await modelScript.waitForSteps(1)
    const banner = visibleControlBanner(page)
    const form = page.getByTestId('elicitation-form').filter({ visible: true })
    await expect(form).toBeVisible()
    await expect(banner).toContainText('Name the probe.')
    await form.getByLabel('Name *').fill('kiro-e2e')
    await page.getByTestId('control-allow-btn').filter({ visible: true }).click()
    await expect(banner).toHaveCount(0)
    await modelScript.waitForSteps()
    await waitForAgentIdle(page)
    expect(JSON.stringify((await modelScript.status()).requests.at(-1)?.body)).toContain('FORM_ROUND_TRIP_OK')
    await expect(assistantBubbles(page).filter({ hasText: 'The form came back.' })).toBeVisible()
  })
})

/**
 * Open a Kiro agent whose workspace configures the form server. The Allow all
 * policy runs the MCP tool without a permission request, so the form is the only
 * request the call raises.
 */
async function openKiroAgentWithServer(server: Parameters<typeof openKiroAgent>[0], workspaceId: string): Promise<{ workingDir: string }> {
  return openKiroAgent(server, workspaceId, { policyPreset: 'allow-all' }, (workingDir) => {
    const script = join(workingDir, 'form-server.mjs')
    writeFileSync(script, FORM_SERVER)
    const settings = join(workingDir, '.kiro', 'settings')
    mkdirSync(settings, { recursive: true })
    writeFileSync(join(settings, 'mcp.json'), JSON.stringify({ mcpServers: { probe: { command: process.execPath, args: [script] } } }))
  })
}
