import { writeFileSync } from 'node:fs'
import { join } from 'node:path'
import { AgentProvider } from '../../src/generated/proto/leapmux/v1/agent_pb'
import { expect, test } from './fixtures'
import { openAgentViaAPI } from './helpers/api'
import { withMockModelScenario } from './helpers/mockModelScenario'
import { mcpToolCall } from './helpers/providerToolCalls'
import { createTestDirectory } from './helpers/runDirectory'
import { withMockPiModel } from './helpers/scriptedPiModel'
import { readEntry, storageKeys } from './helpers/storage'
import { expectSettingsChip, messageBubbles, openWorkspace, sendMessage, waitForAgentIdle } from './helpers/ui'

const serverScript = `
import { createInterface } from 'node:readline';
let toolRequest;
const send = value => process.stdout.write(JSON.stringify(value) + '\\n');
for await (const line of createInterface({ input: process.stdin })) {
  const request = JSON.parse(line);
  if (request.id === undefined) continue;
  if (request.id === 'probe-form' && !request.method) {
    const reply = request.result;
    const valid = reply?.action === 'accept' && reply.content?.count === 0 && reply.content?.enabled === false && reply.content?.color === 'b';
    send({jsonrpc:'2.0',id:toolRequest,result:{content:[{type:'text',text:valid ? 'FORM_ROUND_TRIP_OK' : 'FORM_ROUND_TRIP_FAILED'}]}});
    continue;
  }
  let result;
  switch (request.method) {
    case 'initialize':
      result = {protocolVersion:request.params.protocolVersion,capabilities:{tools:{}},serverInfo:{name:'form_probe',version:'1'}};
      break;
    case 'tools/list':
      result = {tools:[{name:'ask',description:'Request the disposable probe form. Call once with no arguments.',inputSchema:{type:'object',properties:{}}},{name:'echo',description:'Echo approved arguments.',inputSchema:{type:'object',properties:{query:{type:'string'},limit:{type:'integer'},tail:{type:'string'}},required:['query','limit','tail']}}]};
      break;
    case 'tools/call':
      if (request.params.name === 'echo') {
        const valid = request.params.arguments?.limit === 0 && request.params.arguments?.tail === 'END_MCP_ARGUMENTS';
        result = {content:[{type:'text',text:valid ? 'PERMISSION_ACCEPTED' : 'PERMISSION_ARGUMENTS_FAILED'}]};
        break;
      }
      toolRequest = request.id;
      send({jsonrpc:'2.0',id:'probe-form',method:'elicitation/create',params:{mode:'form',message:'Choose the probe settings.',requestedSchema:{type:'object',required:['count','enabled','color'],properties:{count:{type:'integer',title:'Count',minimum:0,maximum:3},enabled:{type:'boolean',title:'Enabled'},color:{type:'string',title:'Color',oneOf:[{const:'b',title:'Blue'},{const:'r',title:'Red'}]}}}}});
      continue;
    default:
      send({jsonrpc:'2.0',id:request.id,error:{code:-32601,message:'Method not supported'}});
      continue;
  }
  send({jsonrpc:'2.0',id:request.id,result});
}
`

// REMOVED: "answers a native Reasonix MCP form and preserves draft values after
// reload". Reasonix no longer forwards the MCP elicitation request over ACP --
// the `form_probe / ask` row stays running while the worker log carries no
// elicitation traffic at all, though both sides advertise the capability. The
// test asserted a surface the provider never reaches, so it failed for a reason
// no change here can fix. See https://github.com/leapmux/leapmux/issues/488,
// which records the evidence and what to restore.
//
// The Pi case below still covers LeapMux's own elicitation rendering.

test('recovers Pi MCP permission arguments and sends the selected approval scope', async ({ page, authenticatedEmptyWorkspace, leapmuxServer }) => {
  const directory = createTestDirectory('pi-mcp-permission-')
  const script = join(directory, 'permission-server.mjs')
  writeFileSync(script, serverScript)
  writeFileSync(join(directory, '.mcp.json'), JSON.stringify({ settings: { approveTools: true }, mcpServers: { form_probe: { command: process.execPath, args: [script] } } }))
  const args = { query: 'x'.repeat(900), limit: 0, tail: 'END_MCP_ARGUMENTS' }
  await withMockPiModel(directory, leapmuxServer.mockModelUrl, async (settings) => {
    const agentId = await openAgentViaAPI(leapmuxServer.hubUrl, leapmuxServer.adminToken, leapmuxServer.workerId, authenticatedEmptyWorkspace.workspaceId, directory, {
      agentProvider: AgentProvider.PI,
      ...settings,
    })
    await openWorkspace(page, authenticatedEmptyWorkspace.workspaceId)
    await expectSettingsChip(page, 'Protocol test')
    await withMockModelScenario(leapmuxServer.mockModelUrl, [
      { toolCalls: [mcpToolCall(AgentProvider.PI, 'mcp-call', { server: 'form_probe', tool: 'echo', input: args })] },
      { text: 'Protocol test complete.' },
    ], async (scenario) => {
      await sendMessage(page, scenario.prompt('Run the configured MCP permission probe.'))
      const banner = page.getByTestId('control-banner').filter({ visible: true })
      await expect(banner).toContainText('Permission Required')
      await expect(banner.locator('pre')).toContainText('END_MCP_ARGUMENTS')
      const scope = page.getByRole('radio', { name: 'Session', exact: true })
      await scope.click()
      // IndexedDB writes are asynchronous. Verify the committed draft before testing reload recovery.
      await expect.poll(async () => {
        const key = (await storageKeys(page)).find(key => key.includes(`control-state:${agentId}:`))
        const value = key ? (await readEntry(page, key))?.v as { choices?: Record<string, string> } | undefined : undefined
        return value?.choices?.['elicitation-accept-choice']
      }).toBe('session')
      await page.reload()
      await expect(banner.locator('pre')).toContainText('END_MCP_ARGUMENTS')
      await expect(scope).toBeChecked()
      await page.getByTestId('control-actions').getByRole('button', { name: 'Allow', exact: true }).click()
      await waitForAgentIdle(page, 120_000)
      await expect(messageBubbles(page).filter({ hasText: 'PERMISSION_ACCEPTED' }).first()).toBeVisible()
      await expect(messageBubbles(page).filter({ hasText: 'Approved for this session' }).first()).toBeVisible()
      await expect(banner).toHaveCount(0)
    })
  })
})
