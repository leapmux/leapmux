import { existsSync, readFileSync } from 'node:fs'
import { join } from 'node:path'
import { create, fromBinary, toBinary } from '@bufbuild/protobuf'
import { AgentProvider, ListAgentMessagesRequestSchema, ListAgentMessagesResponseSchema, WatchReplayMode } from '../../src/generated/proto/leapmux/v1/agent_pb'
import { WatchEventsRequestSchema, WatchEventsResponseSchema, WatchMode } from '../../src/generated/proto/leapmux/v1/workspace_pb'
import { decompressContentToString } from '../../src/lib/decompress'
import { DIRAC_E2E_SKIP_REASON, expect as diracExpect, diracTest, openDiracAgent } from './dirac-fixtures'
import { FAST_AGENT_E2E_SKIP_REASON, fastAgentTest, expect as fastExpect, openFastAgentAgent } from './fastagent-fixtures'
import { getTestChannel } from './helpers/api'
import { writeJunieMcpConfig } from './helpers/junieMcp'
import { writeMcpFormServer } from './helpers/mcpFormServer'
import { writeMcpImageServer } from './helpers/mcpImageServer'
import { bashToolCall, diracRespondToolCall, junieAnswerToolCall, lettaViewImageToolCall, mcpToolCall } from './helpers/providerToolCalls'
import { expectSteeredReply, steerQueuedInput } from './helpers/steer'
import { goalsAndTodosSection } from './helpers/subagentRegistry'
import { toolRows, writeToolImage } from './helpers/toolImages'
import { chooseSettingsOption, expectSettingsOptionChosen, messageBubbles, openWorkspace, sendMessage, waitForAgentIdle, waitForSettingsHydrated, waitForSettingsIdle } from './helpers/ui'
import { JUNIE_E2E_SKIP_REASON, expect as junieExpect, junieTest, openJunieAgent } from './junie-fixtures'
import { LETTA_E2E_SKIP_REASON, LETTA_TITLE_RULE, expect as lettaExpect, lettaTest } from './letta-fixtures'

junieTest.describe('Junie MCP input form', () => {
  junieTest.skip(!!JUNIE_E2E_SKIP_REASON, JUNIE_E2E_SKIP_REASON || '')

  junieTest('declines a local MCP form request without opening a browser form', async ({ page, authenticatedEmptyWorkspace, leapmuxServer, modelScript }, testInfo) => {
    let responseLog = ''
    await openJunieAgent(leapmuxServer, authenticatedEmptyWorkspace.workspaceId, { brave_mode: 'on' }, (workingDir) => {
      responseLog = join(workingDir, 'form-response.json')
      const script = writeMcpFormServer(workingDir, 'form-server.mjs', { responseLog })
      writeJunieMcpConfig(workingDir, 'form_probe', process.execPath, [script])
    })
    await openWorkspace(page, authenticatedEmptyWorkspace.workspaceId)
    await modelScript.rule(
      { name: 'junie-mcp-capability', when: { system: 'capability filter agent' }, respond: { text: '1' } },
      { name: 'junie-mcp-task-name', when: { system: 'task description summarizer' }, respond: { text: 'MCP form task' } },
    )
    await modelScript.queue(
      { toolCalls: [mcpToolCall(AgentProvider.JUNIE, 'junie-mcp-form', { server: 'form_probe', tool: 'ask', input: {} })] },
      { toolCalls: [junieAnswerToolCall('junie-mcp-answer', 'The form completed.')] },
    )
    await sendMessage(page, modelScript.prompt('Call the form_probe ask tool once.'))
    const status = await modelScript.waitForSteps()
    await waitForAgentIdle(page, 120_000)
    await junieExpect.poll(() => existsSync(responseLog)).toBe(true)
    const nativeReply = JSON.parse(readFileSync(responseLog, 'utf8')) as { error?: { code?: number, message?: string } }
    await testInfo.attach('junie-mcp-form-native-reply', { body: JSON.stringify(nativeReply), contentType: 'application/json' })
    junieExpect(nativeReply.error).toMatchObject({ code: -32601, message: 'Server does not support elicitation/create' })
    junieExpect(JSON.stringify(status.requests.find(request => request.stepIndex === 1)?.body)).toContain('FORM_ROUND_TRIP_FAILED')
    await junieExpect(page.getByTestId('elicitation-form').filter({ visible: true })).toHaveCount(0)
  })
})

lettaTest.describe('Letta Code images in tool results', () => {
  lettaTest.skip(!!LETTA_E2E_SKIP_REASON, LETTA_E2E_SKIP_REASON || '')

  lettaTest('keeps the PNG in model input but receives text-only live and stored rows', async ({ authenticatedVisionLettaWorkspace, page, modelScript, leapmuxServer }, testInfo) => {
    const workingDir = authenticatedVisionLettaWorkspace.workingDir
    if (!workingDir)
      throw new Error('the Letta workspace has no working directory')
    const imageName = writeToolImage(workingDir, 'letta')
    const agentId = await page.locator('[data-testid="tab"][data-tab-type="agent"]').first().getAttribute('data-tab-id') ?? ''
    const channel = await getTestChannel(leapmuxServer.hubUrl, leapmuxServer.adminToken)
    const channelId = await channel.getOrOpenChannel(leapmuxServer.workerId)
    const request = create(WatchEventsRequestSchema, {
      agents: [{ agentId, mode: WatchMode.FULL, replay: WatchReplayMode.LATEST, cursorSeq: 0n }],
      updateId: 1n,
    })
    const watch = channel.stream(channelId, 'WatchEvents', toBinary(WatchEventsRequestSchema, request))
    const liveRows: string[] = []
    let subscribed = false
    watch.onMessage((frame) => {
      const response = fromBinary(WatchEventsResponseSchema, frame.payload)
      if (response.event.case === 'updateAck') {
        subscribed = response.event.value.updateId === 1n && response.event.value.rejectedAgents.length === 0
        return
      }
      if (response.event.case !== 'agentEvent')
        return
      const event = response.event.value
      if (event.agentId !== agentId || event.replay || event.event.case !== 'agentMessage')
        return
      const message = event.event.value
      const raw = decompressContentToString(message.content, message.contentCompression)
      if (raw?.includes('tool_return_message'))
        liveRows.push(raw)
    })
    try {
      await lettaExpect.poll(() => subscribed).toBe(true)
      await modelScript.rule(LETTA_TITLE_RULE)
      await modelScript.queue(
        { toolCalls: [lettaViewImageToolCall('letta-view-image', join(workingDir, imageName))] },
        { text: 'I inspected the picture.' },
      )
      await sendMessage(page, modelScript.prompt(`Open ${imageName} with ViewImage.`))
      const status = await modelScript.waitForSteps()
      await waitForAgentIdle(page, 180_000)
      lettaExpect(JSON.stringify(status.requests.find(record => record.stepIndex === 1)?.body)).toContain('iVBORw0KGgo')

      const transcript = await channel.callWorker(leapmuxServer.workerId, 'ListAgentMessages', ListAgentMessagesRequestSchema, ListAgentMessagesResponseSchema, { agentId, limit: 200 })
      const storedRows = transcript.messages.map(message => decompressContentToString(message.content, message.contentCompression)).filter((content): content is string => !!content && content.includes('tool_return_message'))
      await testInfo.attach('letta-live-tool-results', { body: JSON.stringify(liveRows, null, 2), contentType: 'application/json' })
      await testInfo.attach('letta-stored-tool-results', { body: JSON.stringify(storedRows, null, 2), contentType: 'application/json' })
      lettaExpect(liveRows.length).toBeGreaterThan(0)
      lettaExpect(storedRows.length).toBeGreaterThan(0)
      lettaExpect(liveRows.some(row => row.includes('iVBORw0KGgo'))).toBe(false)
      lettaExpect(storedRows.some(row => row.includes('iVBORw0KGgo'))).toBe(false)
      await lettaExpect(toolRows(page).filter({ hasText: 'ViewImage' }).first()).toBeVisible()
      await lettaExpect(page.locator('[data-chat-scroll-container="true"]:visible button[aria-label="Open image"]')).toHaveCount(0)
    }
    finally {
      watch.cancel()
    }
  })
})

junieTest.describe('Junie images in tool results', () => {
  junieTest.skip(!!JUNIE_E2E_SKIP_REASON, JUNIE_E2E_SKIP_REASON || '')

  junieTest('receives no image bytes in the ACP tool row after the model sees the PNG', async ({ page, authenticatedEmptyWorkspace, leapmuxServer, modelScript }, testInfo) => {
    let imageName = ''
    await openJunieAgent(leapmuxServer, authenticatedEmptyWorkspace.workspaceId, { brave_mode: 'on' }, (workingDir) => {
      imageName = writeToolImage(workingDir, 'junie-mcp')
      const server = writeMcpImageServer(workingDir, imageName)
      writeJunieMcpConfig(workingDir, 'image_probe', server.command, server.args)
    })
    await openWorkspace(page, authenticatedEmptyWorkspace.workspaceId)
    await modelScript.rule(
      { name: 'junie-image-capability', when: { system: 'capability filter agent' }, respond: { text: '1' } },
      { name: 'junie-image-task-name', when: { system: 'task description summarizer' }, respond: { text: 'MCP image task' } },
    )
    await modelScript.queue(
      { toolCalls: [mcpToolCall(AgentProvider.JUNIE, 'junie-mcp-image', { server: 'image_probe', tool: 'show', input: {} })] },
      { toolCalls: [junieAnswerToolCall('junie-image-answer', 'The MCP image is ready.')] },
    )
    await sendMessage(page, modelScript.prompt('Call the image_probe show tool once.'))
    const status = await modelScript.waitForSteps()
    await waitForAgentIdle(page, 120_000)
    junieExpect(JSON.stringify(status.requests.find(request => request.stepIndex === 1)?.body)).toContain('iVBORw0KGgo')
    const agentId = await page.locator('[data-testid="tab"][data-tab-type="agent"]').first().getAttribute('data-tab-id') ?? ''
    const channel = await getTestChannel(leapmuxServer.hubUrl, leapmuxServer.adminToken)
    const transcript = await channel.callWorker(leapmuxServer.workerId, 'ListAgentMessages', ListAgentMessagesRequestSchema, ListAgentMessagesResponseSchema, { agentId, limit: 200 })
    const rows = transcript.messages.map(message => ({
      spanType: message.spanType,
      content: decompressContentToString(message.content, message.contentCompression),
    }))
    await testInfo.attach('junie-mcp-image-worker-rows', { body: JSON.stringify(rows, null, 2), contentType: 'application/json' })
    const completed = rows.map((row) => {
      if (!row.content)
        return null
      return JSON.parse(row.content) as {
        sessionUpdate?: string
        title?: string
        status?: string
        content?: unknown[]
        _meta?: { is_mcp_tool_call?: boolean }
      }
    }).find(row => row?.sessionUpdate === 'tool_call_update'
      && row.title === 'image_probe/show'
      && row.status === 'completed'
      && row._meta?.is_mcp_tool_call === true)
    junieExpect(completed).toBeDefined()
    junieExpect(completed?.content).toEqual([])
    await junieExpect(page.locator('[data-chat-scroll-container="true"]:visible button[aria-label="Open image"]')).toHaveCount(0)
  })
})

diracTest.describe('Dirac model and steering', () => {
  diracTest.skip(!!DIRAC_E2E_SKIP_REASON, DIRAC_E2E_SKIP_REASON || '')

  diracTest('sends a selected model on the next request and keeps it after reload', async ({ page, authenticatedEmptyWorkspace, leapmuxServer, modelScript }) => {
    await openDiracAgent(leapmuxServer, authenticatedEmptyWorkspace.workspaceId)
    await openWorkspace(page, authenticatedEmptyWorkspace.workspaceId)
    await waitForSettingsHydrated(page)
    await chooseSettingsOption(page, 'model-gpt-6-astra')
    await waitForSettingsIdle(page)
    await expectSettingsOptionChosen(page, 'model-gpt-6-astra')

    await modelScript.queue({ toolCalls: [diracRespondToolCall('dirac-model-answer', 'complete', 'The selected model answered.')] })
    await sendMessage(page, modelScript.prompt('Reply once with the selected model.'))
    const status = await modelScript.waitForSteps()
    await waitForAgentIdle(page, 120_000)
    const request = status.requests.find(record => record.stepIndex === 0)
    diracExpect(request?.protocol).toBe('openai-chat-completions')
    diracExpect(request?.body).toMatchObject({ model: 'gpt-6-astra' })

    await page.reload()
    await waitForSettingsHydrated(page)
    await expectSettingsOptionChosen(page, 'model-gpt-6-astra')
  })

  diracTest('puts a native whisper into the active turn', async ({ page, authenticatedEmptyWorkspace, leapmuxServer, modelScript }) => {
    await openDiracAgent(leapmuxServer, authenticatedEmptyWorkspace.workspaceId)
    await openWorkspace(page, authenticatedEmptyWorkspace.workspaceId)
    const gate = 'dirac-whisper-gate'
    const steering = 'Include STEEREDWORD in the answer.'
    await modelScript.queue(
      { gate, toolCalls: [bashToolCall(AgentProvider.DIRAC, 'dirac-steer-tool', 'printf dirac-steer-ready')] },
      { toolCalls: [diracRespondToolCall('dirac-steer-answer', 'complete', 'The answer includes STEEREDWORD.')] },
    )
    await sendMessage(page, modelScript.prompt('Run the scripted command, then answer.'))
    await modelScript.waitForGate(gate)
    try {
      await steerQueuedInput(page, { message: steering, match: 'Include STEEREDWORD' })
    }
    finally {
      await modelScript.releaseGate(gate)
    }
    const status = await modelScript.waitForSteps()
    await waitForAgentIdle(page, 120_000)
    diracExpect(JSON.stringify(status.requests.find(request => request.stepIndex === 1)?.body)).toContain(steering)
    await expectSteeredReply(page, 'STEEREDWORD', 'last')
  })
})

fastAgentTest.describe('Fast Agent to-do support', () => {
  fastAgentTest.skip(!!FAST_AGENT_E2E_SKIP_REASON, FAST_AGENT_E2E_SKIP_REASON || '')

  fastAgentTest('offers no native to-do command or tool in the launched coding agent', async ({ page, authenticatedEmptyWorkspace, leapmuxServer, modelScript }) => {
    await openFastAgentAgent(leapmuxServer, authenticatedEmptyWorkspace.workspaceId)
    await openWorkspace(page, authenticatedEmptyWorkspace.workspaceId)

    // Fast Agent's /commands route reads the same available-command catalogue
    // that its ACP session publishes. An unknown /todo route must refuse.
    await sendMessage(page, '/commands --json')
    await waitForAgentIdle(page, 120_000)
    const commands = messageBubbles(page).filter({ hasText: 'command_index' }).first()
    await fastExpect(commands).toBeVisible()
    fastExpect(await commands.textContent()).not.toMatch(/"name"\s*:\s*"(?:todo|todowrite|update_plan|plan_update|task_list)"/i)
    await sendMessage(page, '/todo')
    await waitForAgentIdle(page, 120_000)
    await fastExpect(messageBubbles(page).filter({ hasText: 'Unknown command: /todo' }).first()).toBeVisible()

    await modelScript.queue({ text: 'The coding turn answered without a plan update.' })
    await sendMessage(page, modelScript.prompt('Create a native to-do list if a tool supports it.'))
    const status = await modelScript.waitForSteps()
    await waitForAgentIdle(page, 120_000)
    const request = status.requests.find(record => record.stepIndex === 0)
    fastExpect(request).toBeDefined()
    const tools = JSON.stringify(request?.body)
    fastExpect(tools).toContain('"tools"')
    fastExpect(tools).not.toMatch(/"name"\s*:\s*"(?:todo|todowrite|update_plan|plan_update|task_list)"/i)
    await fastExpect(goalsAndTodosSection(page)).toHaveCount(0)
  })
})
