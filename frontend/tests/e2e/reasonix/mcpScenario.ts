import type { NativeContextFixtures } from '../helpers/nativeScenario'
import { writeFileSync } from 'node:fs'
import { join } from 'node:path'
import { agentOpenOptions } from '../agentSettings'
import { openAgentViaAPI } from '../helpers/api'
import { writeMcpFormServer } from '../helpers/mcpFormServer'
import { exerciseMcpProbeFormRoundTrip } from '../helpers/mcpProbeForm'
import { mcpServersConfig } from '../helpers/mcpProbeServer'
import { newNativeWorkingDir } from '../helpers/nativeAgentOpen'
import { openWorkspace } from '../helpers/ui'
import { nativeContext } from './scenarios'

/**
 * Open a Reasonix agent whose project registers the probe form server, and answer the form through the browser after
 * a reload. The Yolo approval runs the MCP tool without a request, so the form is the only request of the call.
 */
export async function exerciseReasonixMcpForm(fixtures: NativeContextFixtures): Promise<void> {
  const context = await nativeContext(fixtures)
  const directory = newNativeWorkingDir(context, 'reasonix-mcp-form-')
  writeFileSync(join(directory, '.mcp.json'), JSON.stringify(mcpServersConfig(writeMcpFormServer(directory, 'form-server.mjs'))))
  const { leapmuxServer } = context
  await openAgentViaAPI(leapmuxServer.hubUrl, leapmuxServer.adminToken, leapmuxServer.workerId, context.workspaceId, directory, agentOpenOptions(context.provider, {
    optionValues: { tool_approval: 'yolo' },
  }))
  await openWorkspace(context.page, context.workspaceId)
  await exerciseMcpProbeFormRoundTrip(context, { callId: 'mcp-form', reloadBeforeSubmit: true })
}
