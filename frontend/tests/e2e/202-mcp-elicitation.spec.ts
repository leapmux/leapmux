import { execFileSync } from 'node:child_process'
import { writeFileSync } from 'node:fs'
import { join } from 'node:path'
import { AgentProvider } from '../../src/generated/proto/leapmux/v1/agent_pb'
import { expect, test } from './fixtures'
import { openAgentViaAPI } from './helpers/api'
import { createTestDirectory } from './helpers/runDirectory'
import { withScriptedPiTool } from './helpers/scriptedPiModel'
import { readEntry, storageKeys } from './helpers/storage'
import { expectSettingsChip, messageBubbles, openWorkspace, sendMessage, waitForAgentIdle, waitForSettingsHydrated } from './helpers/ui'
import { realAgentOpenOptions, realAgentSettings } from './realAgentSettings'

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

test('answers a native Reasonix MCP form and preserves draft values after reload', async ({ page, authenticatedEmptyWorkspace, leapmuxServer }) => {
  const directory = createTestDirectory('reasonix-mcp-form-')
  const script = join(directory, 'form-server.mjs')
  writeFileSync(script, serverScript)
  writeFileSync(join(directory, '.mcp.json'), JSON.stringify({ mcpServers: { form_probe: { command: process.execPath, args: [script] } } }))
  const provider = AgentProvider.REASONIX
  const agentId = await openAgentViaAPI(leapmuxServer.hubUrl, leapmuxServer.adminToken, leapmuxServer.workerId, authenticatedEmptyWorkspace.workspaceId, directory, {
    agentProvider: provider,
    ...realAgentOpenOptions(realAgentSettings(provider)),
    optionValues: { tool_approval: 'yolo' },
  })
  await openWorkspace(page, authenticatedEmptyWorkspace.workspaceId)
  await waitForSettingsHydrated(page)
  await waitForAgentIdle(page)
  await sendMessage(page, 'Call the form_probe MCP ask tool exactly once with no arguments. Use capability discovery if needed. Do not use other tools. Report its exact result.')
  const form = page.getByTestId('elicitation-form').filter({ visible: true })
  await expect(form).toBeVisible()
  const pendingRequests = () => JSON.parse(execFileSync('sqlite3', ['-json', join(leapmuxServer.dataDir, 'worker', 'worker.db'), `SELECT request_id, claim_token, CAST(payload AS TEXT) AS payload FROM control_requests WHERE agent_id='${agentId.replaceAll('\'', '\'\'')}'`], { encoding: 'utf8' }) || '[]') as unknown[]
  const beforeReload = pendingRequests()
  expect(beforeReload).toHaveLength(1)
  await form.getByLabel('Count *').fill('0')
  await form.getByRole('button', { name: 'Enabled *', exact: true }).click()
  await page.getByRole('menuitemradio', { name: 'No', exact: true }).click()
  await form.getByRole('button', { name: 'Color *', exact: true }).click()
  await page.getByRole('menuitemradio', { name: 'Blue', exact: true }).click()
  // The final choice must reach durable storage before reload can test recovery.
  await expect.poll(async () => {
    const key = (await storageKeys(page)).find(key => key.includes(`control-state:${agentId}:`))
    const value = key ? (await readEntry(page, key))?.v as { choices?: Record<string, string> } | undefined : undefined
    return value?.choices
  }).toEqual({ 'elicitation:"count"': '0', 'elicitation:"enabled"': 'false', 'elicitation:"color"': '"b"' })
  await page.reload()
  expect(pendingRequests()).toEqual(beforeReload)
  await expect(form.getByLabel('Count *')).toHaveValue('0')
  await expect(form.getByRole('button', { name: 'Enabled *', exact: true })).toHaveText('No')
  await expect(form.getByRole('button', { name: 'Color *', exact: true })).toHaveText('Blue')
  await page.getByTestId('control-actions').getByRole('button', { name: 'Approve', exact: true }).click()
  await waitForAgentIdle(page, 120_000)
  await expect(messageBubbles(page).filter({ hasText: 'FORM_ROUND_TRIP_OK' }).first()).toBeVisible()
  await expect(form).toHaveCount(0)
})

test('recovers Pi MCP permission arguments and sends the selected approval scope', async ({ page, authenticatedEmptyWorkspace, leapmuxServer }) => {
  const directory = createTestDirectory('pi-mcp-permission-')
  const script = join(directory, 'permission-server.mjs')
  writeFileSync(script, serverScript)
  writeFileSync(join(directory, '.mcp.json'), JSON.stringify({ settings: { approveTools: true }, mcpServers: { form_probe: { command: process.execPath, args: [script] } } }))
  const args = { query: 'x'.repeat(900), limit: 0, tail: 'END_MCP_ARGUMENTS' }
  await withScriptedPiTool(directory, 'mcp', { tool: 'form_probe_echo', args }, async (settings) => {
    const agentId = await openAgentViaAPI(leapmuxServer.hubUrl, leapmuxServer.adminToken, leapmuxServer.workerId, authenticatedEmptyWorkspace.workspaceId, directory, {
      agentProvider: AgentProvider.PI,
      ...settings,
    })
    await openWorkspace(page, authenticatedEmptyWorkspace.workspaceId)
    await expectSettingsChip(page, 'Protocol test')
    await sendMessage(page, 'Run the configured MCP permission probe.')
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
    await page.getByTestId('control-actions').getByRole('button', { name: 'Approve', exact: true }).click()
    await waitForAgentIdle(page, 120_000)
    await expect(messageBubbles(page).filter({ hasText: 'PERMISSION_ACCEPTED' }).first()).toBeVisible()
    await expect(messageBubbles(page).filter({ hasText: 'Approved for this session' }).first()).toBeVisible()
    await expect(banner).toHaveCount(0)
  })
})
