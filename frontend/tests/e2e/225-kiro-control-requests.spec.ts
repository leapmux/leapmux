import type { Page } from '@playwright/test'
import { existsSync, mkdirSync, readdirSync, readFileSync, writeFileSync } from 'node:fs'
import { join } from 'node:path'
import process from 'node:process'
import { OPTION_ID_PERMISSION_MODE } from '../../src/components/chat/settingsGroups'
import { AgentProvider } from '../../src/generated/proto/leapmux/v1/agent_pb'
import { kiroUserText } from './helpers/kiroSurface'
import { askUserQuestionToolCall, bashToolCall, mcpToolCall } from './helpers/providerToolCalls'
import { assistantBubbles, expectSettingsChip, messageBubbles, openWorkspace, sendMessage, waitForAgentIdle } from './helpers/ui'
import { expect, KIRO_E2E_SKIP_REASON, kiroTest, openKiroAgent } from './kiro-fixtures'

kiroTest.skip(!!KIRO_E2E_SKIP_REASON, KIRO_E2E_SKIP_REASON || '')

const PROVIDER = AgentProvider.KIRO

function controlBanner(page: Page) {
  return page.getByTestId('control-banner').filter({ visible: true })
}

/** The text of a file, or '' for a file that does not exist. */
function readIfPresent(path: string): string {
  return existsSync(path) ? readFileSync(path, 'utf8') : ''
}

/**
 * The permission rules that Kiro keeps for each workspace, under its own HOME, by
 * the directory of the workspace: `.kiro/workspace-roots/<hash>/permissions.yaml`.
 */
function workspaceRuleFiles(home: string): Record<string, string> {
  const roots = join(home, '.kiro', 'workspace-roots')
  if (!existsSync(roots))
    return {}
  return Object.fromEntries(readdirSync(roots).map(hash => [hash, readIfPresent(join(roots, hash, 'permissions.yaml'))]))
}

/**
 * A disposable MCP server whose one tool asks for a form.
 *
 * It writes `ready` beside itself when Kiro lists its tools, which is the moment the
 * tool exists for the model, and it answers the call with whether the form came
 * back with the value the test typed.
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

/**
 * 225 -- Kiro control requests.
 *
 * Kiro raises a tool permission as the standard request, a question as its own
 * `_kiro/userInput`, and an MCP form as its own `_kiro/mcp/elicitation`. Each one
 * draws the shared surface, and each answer reaches Kiro in the shape it reads.
 */
kiroTest.describe('Kiro control requests', () => {
  // Kiro's own rules ask before a command that writes a file. A rejection that
  // carries a reason puts it in Kiro's own `_meta.kiro.rejectionReason`, and Kiro
  // hands the reason to the model inside the same turn.
  kiroTest('approves one command and rejects the next with a reason the turn reads', async ({ page, authenticatedEmptyWorkspace, leapmuxServer, modelScript }) => {
    const { workingDir } = await openKiroAgent(leapmuxServer, authenticatedEmptyWorkspace.workspaceId)
    await openWorkspace(page, authenticatedEmptyWorkspace.workspaceId)
    const approved = join(workingDir, 'approved.txt')
    const rejected = join(workingDir, 'rejected.txt')

    await modelScript.queue(
      { toolCalls: [bashToolCall(PROVIDER, 'kiro-approved', `printf approved > ${approved}`)] },
      { toolCalls: [bashToolCall(PROVIDER, 'kiro-rejected', `printf rejected > ${rejected}`)] },
      { text: 'I read the reason and stopped.' },
    )
    await sendMessage(page, modelScript.prompt('Create the two scripted files.'))
    await modelScript.waitForSteps(1)
    const banner = controlBanner(page)
    await expect(banner).toContainText(`printf approved > ${approved}`)
    await page.getByTestId('control-allow-btn').filter({ visible: true }).click()

    await modelScript.waitForSteps(2)
    await expect(banner).toContainText(`printf rejected > ${rejected}`)
    expect(existsSync(approved)).toBe(true)
    const editor = page.getByTestId('composer-editor').locator('.ProseMirror')
    await editor.fill('Do not create the second file.')
    await page.keyboard.press('Meta+Enter')
    await expect(banner).toHaveCount(0)
    await modelScript.waitForSteps()
    await waitForAgentIdle(page, 120_000)

    const status = await modelScript.status()
    expect(JSON.stringify(status.requests.at(-1)?.body)).toContain('Do not create the second file.')
    await expect(messageBubbles(page).filter({ hasText: 'Sent feedback:' }).filter({ hasText: 'Do not create the second file.' }).first()).toBeVisible()
    await expect(assistantBubbles(page).filter({ hasText: 'I read the reason and stopped.' })).toBeVisible()
    expect(existsSync(rejected)).toBe(false)
  })

  // Kiro keeps an always-allow for the session unless the reply states a wider
  // scope. The Workspace pill states one: the worker turns LeapMux's own option
  // into Kiro's `always-accept` with the scope, and the same command then runs
  // without a second request. Kiro keeps a session rule in memory alone, and it
  // writes a workspace rule into the rule file of the workspace, which proves the
  // scope.
  kiroTest('keeps an always-allow for the workspace', async ({ page, authenticatedEmptyWorkspace, leapmuxServer, modelScript }) => {
    const { workingDir } = await openKiroAgent(leapmuxServer, authenticatedEmptyWorkspace.workspaceId)
    await openWorkspace(page, authenticatedEmptyWorkspace.workspaceId)
    const marker = join(workingDir, 'always.txt')
    const command = `printf always >> ${marker}`
    const home = leapmuxServer.agentEnv.HOME!
    const userRuleFile = join(home, '.kiro', 'settings', 'permissions.yaml')
    const workspaceRulesBefore = workspaceRuleFiles(home)
    const userRulesBefore = readIfPresent(userRuleFile)

    await modelScript.queue(
      { toolCalls: [bashToolCall(PROVIDER, 'kiro-always-1', command)] },
      { toolCalls: [bashToolCall(PROVIDER, 'kiro-always-2', command)] },
      { text: 'Both ran.' },
    )
    await sendMessage(page, modelScript.prompt('Run the scripted command twice.'))
    await modelScript.waitForSteps(1)
    const banner = controlBanner(page)
    await expect(banner).toContainText(command)
    // The scope pills sit in the control actions of the composer, not in the banner.
    await page.getByTestId('control-actions').filter({ visible: true }).getByRole('radio', { name: 'Workspace', exact: true }).click()
    await page.getByTestId('control-allow-btn').filter({ visible: true }).click()
    await expect(banner).toHaveCount(0)

    // The second call runs under the rule, with no request of its own.
    await modelScript.waitForSteps()
    await waitForAgentIdle(page, 120_000)
    await expect(assistantBubbles(page).filter({ hasText: 'Both ran.' })).toBeVisible()
    await expect(messageBubbles(page).filter({ hasText: 'Always allow in this workspace' }).first()).toBeVisible()
    expect(readFileSync(marker, 'utf8'), 'both commands ran').toBe('alwaysalways')

    // A session rule writes no file, and a rule for the user writes the rule file of
    // the user. The workspace of this test is new, so its rule file is the one file
    // that changed.
    const changed = Object.entries(workspaceRuleFiles(home)).filter(([hash, rules]) => rules !== (workspaceRulesBefore[hash] ?? ''))
    expect(changed, 'Kiro keeps the rule for the workspace').toHaveLength(1)
    expect(changed[0]?.[1]).toContain('allow')
    expect(readIfPresent(userRuleFile), 'Kiro keeps no rule for the user').toBe(userRulesBefore)
  })

  // Kiro offers its question tool in a spec mode alone, and a spec mode first
  // classifies the prompt, which the housekeeping rules answer. The test chooses
  // the SECOND option, so an answer that Kiro never read cannot pass as the first.
  kiroTest('answers a question with a chosen option', async ({ page, authenticatedEmptyWorkspace, leapmuxServer, modelScript }) => {
    await openKiroAgent(leapmuxServer, authenticatedEmptyWorkspace.workspaceId, { [OPTION_ID_PERMISSION_MODE]: 'spec' })
    await openWorkspace(page, authenticatedEmptyWorkspace.workspaceId)
    await expectSettingsChip(page, 'Spec')

    await modelScript.queue(
      {
        toolCalls: [askUserQuestionToolCall(PROVIDER, 'kiro-question', [{
          question: 'Which database?',
          header: 'Database',
          options: [{ label: 'Postgres', description: 'Relational' }, { label: 'SQLite', description: 'Embedded' }],
        }])],
      },
      { text: 'SQLite it is.' },
    )
    await sendMessage(page, modelScript.prompt('Ask me for a database.'))
    await modelScript.waitForSteps(1)
    const banner = controlBanner(page)
    await expect(banner).toContainText('Which database?')
    await page.locator('[data-testid="question-option-SQLite"]:visible').click()
    const submit = page.locator('[data-testid="control-submit-btn"]:visible')
    await expect(submit).toBeEnabled()
    await submit.click()
    await expect(banner).toHaveCount(0)
    await modelScript.waitForSteps()
    await waitForAgentIdle(page, 120_000)

    // The call after the question carries the answer as the result of the question
    // tool. The history of the call also lists both options, so the test reads the
    // current message alone.
    const afterAnswer = (await modelScript.status()).requests.find(request => request.stepIndex === 1)
    const answered = kiroUserText(afterAnswer?.body)
    expect(answered).toContain('SQLite')
    expect(answered).not.toContain('Postgres')
    await expect(assistantBubbles(page).filter({ hasText: 'SQLite it is.' })).toBeVisible()
  })

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
    const banner = controlBanner(page)
    const form = page.getByTestId('elicitation-form').filter({ visible: true })
    await expect(form).toBeVisible()
    await expect(banner).toContainText('Name the probe.')
    await form.getByLabel('Name *').fill('kiro-e2e')
    await page.getByTestId('control-allow-btn').filter({ visible: true }).click()
    await expect(banner).toHaveCount(0)
    await modelScript.waitForSteps()
    await waitForAgentIdle(page, 120_000)
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
