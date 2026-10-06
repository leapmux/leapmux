import type { Locator } from '@playwright/test'
import type { ManagedNativeScenarioContext } from '../helpers/nativeScenario'
import { existsSync, readFileSync } from 'node:fs'
import { join } from 'node:path'
import { expect } from '@playwright/test'
import { CODEWHALE_EVENT } from '../../../src/generated/contracts/codewhale-protocol'
import { MessageCompletion } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { isObject } from '../../../src/lib/jsonPick'
import { parseMessageContent } from '../../../src/lib/messageParser'
import { codewhaleTest } from '../codewhale-fixtures'
import { writeMcpImageServer } from '../helpers/mcpImageServer'
import { CODEWHALE_VISION_MODEL_ID } from '../helpers/mockAgentEnvironment'
import { withNativeConfigurationFile } from '../helpers/nativeConfigurationFile'
import { exerciseSessionResume } from '../helpers/nativeLifecycle'
import { readAllAgentMessages } from '../helpers/nativeMessages'
import { currentNativeAgent } from '../helpers/nativeScenario'
import { waitForNativeToolSteps } from '../helpers/nativeToolExecution'
import { nativeToolResult } from '../helpers/nativeToolResult'
import { codewhaleReadMediaToolCall, mcpToolCall } from '../helpers/providerToolCalls'
import { newProviderWorkingDir } from '../helpers/providerWorkingDir'
import { getGlobalState } from '../helpers/server'
import { expectMcpToolImage, expectToolRowImage, mcpResultImage, writeToolImage } from '../helpers/toolImages'
import { applyPermissionPreset, openWorkspace, sendMessage, tabById, waitForSettingsHydrated } from '../helpers/ui'
import { openProviderAgent } from '../helpers/workspace'
import { CODEWHALE_AGENT, nativeContext } from './scenarios'

/** Prove that the actual image bytes reside in the completed Worker's stored row. */
async function expectSavedImage(context: ManagedNativeScenarioContext, callId: string, uri: string): Promise<void> {
  const agent = await currentNativeAgent(context)
  const saved = await readAllAgentMessages(context, agent.id)
  const results = saved.filter(message => message.spanId === callId && message.completion === MessageCompletion.COMPLETE)
  expect(results).toHaveLength(1)
  const parsed = parseMessageContent(results[0]!)
  expect(parsed.topLevel?.event).toBe(CODEWHALE_EVENT.ItemCompleted)
  const payload = parsed.topLevel?.payload
  const item = isObject(payload) ? payload.item : undefined
  const metadata = isObject(item) ? item.metadata : undefined
  const media = isObject(metadata) ? metadata.tool_media : undefined
  expect(Array.isArray(media)).toBe(true)
  expect(media).toHaveLength(1)
  const descriptor = Array.isArray(media) ? media[0] : undefined
  if (!isObject(descriptor) || typeof descriptor.artifact_id !== 'string')
    throw new Error('The completed native image result has no full tool output ID.')
  expect(descriptor.tool_call_id).toBe(callId)
  const supplement = parsed.supplementalContent
  const outputFiles = isObject(supplement) && isObject(supplement.outputFiles) ? supplement.outputFiles : undefined
  expect(outputFiles?.[descriptor.artifact_id]).toBe(uri)
}

/** Open the stored transcript image in its image tab and decode the actual bytes. */
async function expectStoredImageViewer(context: ManagedNativeScenarioContext, image: Locator): Promise<void> {
  const agent = await currentNativeAgent(context)
  await image.locator('xpath=ancestor::button[@aria-label="Open image"]').click()
  const viewer = context.page.locator('img[src^="blob:"]:visible')
  await expect(viewer).toBeVisible()
  await expect.poll(() => viewer.evaluate(element => element instanceof HTMLImageElement ? [element.naturalWidth, element.naturalHeight] : [])).toEqual([64, 64])
  await tabById(context.page, agent.id).click()
}

// The native result identifies an owned image artifact. LeapMux must load its bytes and draw the image.
codewhaleTest('draws the actual native MCP image before and after reload', async ({ page, modelScript, leapmuxServer, authenticatedEmptyWorkspace }) => {
  const workingDir = newProviderWorkingDir(CODEWHALE_AGENT, 'codewhale-native-image-result-')
  const imageName = writeToolImage(workingDir, 'codewhale-native-mcp')
  const server = writeMcpImageServer(workingDir, imageName)
  const config = join(leapmuxServer.agentEnv.CODEWHALE_HOME!, 'mcp.json')
  await withNativeConfigurationFile({ path: config, content: JSON.stringify({ servers: { [server.name]: { command: server.command, args: server.args } } }), runDir: getGlobalState().tmpDir }, async () => {
    const context = await nativeContext({ page, modelScript, leapmuxServer, workspaceId: authenticatedEmptyWorkspace.workspaceId })
    await openProviderAgent(leapmuxServer, context.workspaceId, CODEWHALE_AGENT, { workingDir, model: CODEWHALE_VISION_MODEL_ID })
    await openWorkspace(page, context.workspaceId)
    await applyPermissionPreset(page, 'bypass')
    const start = await modelScript.queue(
      { toolCalls: [mcpToolCall(context.provider, 'image-load', { server: server.name, tool: 'show', input: {} })] },
      { toolCalls: [mcpToolCall(context.provider, 'image-result', { server: server.name, tool: 'show', input: {} })] },
      { text: 'The actual image tool completed.' },
    )
    await sendMessage(page, modelScript.prompt('Load the registered image tool, then call it and consume its result.'))
    // A direct native MCP call initializes the lazy pool. ToolSearch only reads the existing catalog.
    expect(nativeToolResult(await modelScript.requestAt(start + 1), 'image-load')).toContain(`MCP image ${imageName}`)
    await expect.poll(() => existsSync(server.ready)).toBe(true)
    await waitForNativeToolSteps(context, start + 3)
    const request = await modelScript.requestAt(start + 2)
    expect(nativeToolResult(request, 'image-result')).toContain(`MCP image ${imageName}`)
    expect(nativeToolResult(request, 'image-result')).toContain('[MCP image payload removed from text output]')
    const imageBytes = readFileSync(join(workingDir, imageName)).toString('base64')
    expect(JSON.stringify(request.body).includes(imageBytes), 'the model request carries the image bytes').toBe(true)
    await expectSavedImage(context, 'image-result', `data:image/png;base64,${imageBytes}`)
    await expectMcpToolImage(page, imageName, 'image-result')
    await page.reload()
    await waitForSettingsHydrated(page)
    await expectMcpToolImage(page, imageName, 'image-result')
    await exerciseSessionResume(context)
    await expectMcpToolImage(page, imageName, 'image-result')
    await expectStoredImageViewer(context, await mcpResultImage(page, imageName, 'image-result'))
  })
})

codewhaleTest('draws a native ReadMedia image and preserves its stored bytes after reload', async ({ page, modelScript, leapmuxServer, authenticatedEmptyWorkspace }) => {
  const workingDir = newProviderWorkingDir(CODEWHALE_AGENT, 'codewhale-native-read-media-')
  const imageName = writeToolImage(workingDir, 'codewhale-native-read')
  const context = await nativeContext({ page, modelScript, leapmuxServer, workspaceId: authenticatedEmptyWorkspace.workspaceId })
  await openProviderAgent(leapmuxServer, context.workspaceId, CODEWHALE_AGENT, { workingDir, model: CODEWHALE_VISION_MODEL_ID })
  await openWorkspace(page, context.workspaceId)
  await applyPermissionPreset(page, 'bypass')
  const start = await modelScript.queue(
    { toolCalls: [codewhaleReadMediaToolCall('load-read-image', join(workingDir, imageName))] },
    { toolCalls: [codewhaleReadMediaToolCall('read-image', join(workingDir, imageName))] },
    { text: 'The actual ReadMedia image completed.' },
  )
  await sendMessage(page, modelScript.prompt('Run the scripted native image read and consume its result.'))
  await waitForNativeToolSteps(context, start + 3)
  expect(nativeToolResult(await modelScript.requestAt(start + 1), 'load-read-image')).toContain('The tool was not executed. Retry with the loaded schema.')
  const request = await modelScript.requestAt(start + 2)
  expect(nativeToolResult(request, 'read-image')).toContain(imageName)
  const body = request.body
  const messages = isObject(body) && Array.isArray(body.messages) ? body.messages : []
  const images = messages.flatMap((message: unknown) => isObject(message) && Array.isArray(message.content) ? message.content : [])
    .flatMap((block: unknown) => isObject(block) && isObject(block.image_url) && typeof block.image_url.url === 'string' ? [block.image_url.url] : [])
  // The native reader can re-encode its input. Compare the actual model bytes with the stored bytes.
  expect(images).toHaveLength(1)
  expect(images[0]).toMatch(/^data:image\/(?:png|jpeg|gif|webp);base64,/)
  await expectSavedImage(context, 'read-image', images[0]!)
  await expectToolRowImage(page, imageName)
  await page.reload()
  await waitForSettingsHydrated(page)
  await expectToolRowImage(page, imageName)
})
