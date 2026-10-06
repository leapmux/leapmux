import type { TestInfo } from '@playwright/test'
import type { AgentChatMessage } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import type { NativeMessageSnapshot } from '../helpers/nativeMessages'
import type { ManagedNativeScenarioContext } from '../helpers/nativeScenario'
import type { DeepseekHarnessRenderedMcpBlock } from './renderedMcpContent'
import { readFileSync } from 'node:fs'
import { expect } from '@playwright/test'
import { LIMITED_TEXT_DISPLAY_NOTICE, PLAIN_TEXT_DISPLAY_NOTICE } from '../../../src/components/chat/safeTextDisplay'
import { MESSAGE_SUPPLEMENT_FIELD } from '../../../src/generated/contracts/worker-vocab'
import { isObject, pickObject } from '../../../src/lib/jsonPick'
import { shallowEqual } from '../../../src/lib/shallowEqual'
import { readMcpCallArguments } from '../helpers/mcpRequestReceipt'
import { readNativeMessageSnapshot, readNativeToolOutputRecord } from '../helpers/nativeMessages'
import { expandNativeResultView } from '../helpers/nativeResultView'
import { currentNativeAgent } from '../helpers/nativeScenario'
import { copyNativeToolOutputPreview } from '../helpers/nativeToolOutput'
import { nativeOutputPathsPrecedePreview } from '../helpers/nativeToolOutputFilePaths'
import { nativeToolResultContent } from '../helpers/nativeToolResult'
import { openWorkspace, readAttachedWithArgument, toolCallRow } from '../helpers/ui'
import { readDeepseekHarnessNativeOutput } from './outputFilePaths'
import { deepseekHarnessMcpTextDisplay, deepseekHarnessRenderedMcpContent } from './renderedMcpContent'

/** Read the server reply only when its request carries the exact native inspect arguments. */
export function deepseekHarnessInspectReply(receipts: unknown, input: Record<string, unknown>): Record<string, unknown> {
  if (!Array.isArray(receipts))
    throw new Error('The native MCP result receipt must contain an entry array.')
  const entries = receipts.filter(isObject)
  if (entries.length !== receipts.length)
    throw new Error('The native MCP result receipt contains an invalid entry.')
  const requests = entries.map(entry => pickObject(entry, 'request')).filter((request) => {
    const params = pickObject(request, 'params')
    return request?.method === 'tools/call' && params?.name === 'inspect' && isObject(params.arguments) && shallowEqual(params.arguments, input)
  })
  if (requests.length !== 1 || !requests[0] || (typeof requests[0].id !== 'string' && !Number.isSafeInteger(requests[0].id)))
    throw new Error('The native MCP inspect receipt requires one exact request identity.')
  const replies = entries.map(entry => pickObject(entry, 'reply')).filter(reply => reply?.id === requests[0]?.id)
  const result = replies.length === 1 ? pickObject(replies[0], 'result') : undefined
  if (!result)
    throw new Error('The native MCP inspect request has no exact successful server reply.')
  return result
}

/** Read a compact actual MCP value projection from native run_code inline text. */
export function deepseekHarnessCanonicalMcpProjection(frame: unknown, callId: string): Record<string, unknown> {
  const message = isObject(frame) && frame.type === 'tool/result' ? pickObject(pickObject(frame, 'data'), 'message') : undefined
  const content = message?.content
  const block = Array.isArray(content) && content.length === 1 ? content[0] : undefined
  if (!callId || message?.toolCallId !== callId || message.isError !== false || !isObject(block) || block.type !== 'text' || typeof block.text !== 'string')
    throw new Error('The native MCP projection requires an exact successful code result.')
  const [line, ...notices] = block.text.split('\n')
  if (!line || notices.some(notice => notice !== 'File sandbox enforcement is partial on this host.'))
    throw new Error('The native MCP projection contains an unknown code output notice.')
  const value: unknown = JSON.parse(line)
  if (!isObject(value) || typeof value.contentMatches !== 'boolean' || typeof value.textMatches !== 'boolean'
    || typeof value.hasPrivateMeta !== 'boolean' || typeof value.hasNullable !== 'boolean' || value.nullable !== null
    || !Number.isSafeInteger(value.echoedCount) || !Number.isSafeInteger(value.nextCount)
    || typeof value.enabled !== 'boolean' || typeof value.textCharacters !== 'number' || !Number.isSafeInteger(value.textCharacters) || value.textCharacters < 0) {
    throw new Error('The native MCP projection lacks exact structured fields.')
  }
  return value
}

export function deepseekHarnessMcpResultMessage(snapshot: NativeMessageSnapshot, callId: string): AgentChatMessage {
  return readNativeToolOutputRecord(snapshot, {
    callId,
    spanId: callId,
    accepts: frame => frame.type === 'tool/result' && pickObject(pickObject(frame, 'data'), 'message')?.toolCallId === callId,
  }).message
}

/** Read image-only occurrences. Keep every native preview text block unchanged. */
function nativeMcpDisplay(snapshot: NativeMessageSnapshot, callId: string): { display: DeepseekHarnessRenderedMcpBlock[], previewText: string } {
  const result = readDeepseekHarnessNativeOutput(snapshot, callId)
  const provider = isObject(result.supplement) ? pickObject(result.supplement, MESSAGE_SUPPLEMENT_FIELD.Provider) : undefined
  const receipt = pickObject(provider, 'imageAttachments')
  const originals = receipt?.originalImages
  const retained = receipt?.retainedImages
  const images = pickObject(receipt, 'images')
  if (!receipt || receipt.sessionId !== snapshot.agentSessionId || receipt.toolCallId !== callId
    || !Array.isArray(originals) || !Array.isArray(retained) || !images) {
    throw new Error('The native MCP image occurrence receipt lacks its exact owner.')
  }
  const originalRefs = originals.map((position) => {
    const attachment = isObject(position) ? pickObject(position, 'attachment') : undefined
    const id = attachment?.attachmentId
    const value = typeof id === 'string' ? pickObject(images, id) : undefined
    const savedReference = pickObject(value, 'attachment')
    if (!attachment || !value || !savedReference || typeof value.data !== 'string'
      || ['attachmentId', 'mediaType', 'bytes', 'width', 'height'].some(field => savedReference[field] !== attachment[field])) {
      throw new Error('The native MCP image occurrence has another saved image reference.')
    }
    return attachment
  })
  const display: DeepseekHarnessRenderedMcpBlock[] = []
  let next = 0
  for (const block of result.blocks) {
    if (block.type === 'text' && typeof block.text === 'string' && block.text) {
      display.push(...deepseekHarnessMcpTextDisplay(block.text))
    }
    else if (block.type === 'image' && isObject(block.attachment)) {
      let match = next
      while (match < originalRefs.length && originalRefs[match]?.attachmentId !== block.attachment.attachmentId)
        match++
      if (match === originalRefs.length)
        throw new Error('The native MCP retained image does not occur in the original order.')
      while (next <= match)
        display.push({ type: 'image', index: next++ })
    }
  }
  while (next < originalRefs.length)
    display.push({ type: 'image', index: next++ })
  return { display, previewText: result.previewText }
}

/** Check native previews, exact server values, and original image occurrences after reload. */
export async function proveDeepseekHarnessMixedMcpOutput(context: ManagedNativeScenarioContext, options: {
  callId: string
  request: Parameters<typeof nativeToolResultContent>[0]
  expected: readonly ({ type: 'text', text: string } | { type: 'image', path: string })[]
  omittedMarker: string
  firstMarker: string
  retainedImages: number
  input: { count: number, enabled: boolean, text: string }
  receiptLog: string
  testInfo?: Pick<TestInfo, 'attach'>
}): Promise<void> {
  const agent = await currentNativeAgent(context)
  const spillNotice = 'Full formatted result stored at:'
  const excerpt = nativeToolResultContent(options.request, options.callId)
  expect(JSON.stringify(excerpt)).toContain(spillNotice)
  expect(JSON.stringify(excerpt)).not.toContain(options.omittedMarker)
  const initial = await readNativeMessageSnapshot(context, agent.id)
  const receipt = readDeepseekHarnessNativeOutput(initial, options.callId)
  const nativeDisplay = nativeMcpDisplay(initial, options.callId)
  // The native spill notice gives the output path. Both markers must occur in the original preview.
  expect(receipt.paths.length).toBeGreaterThan(0)
  const orderMarkers = [options.firstMarker, spillNotice]
  expect(orderMarkers.map(marker => nativeDisplay.previewText.includes(marker))).toEqual([true, true])
  const expectedOrder = JSON.stringify(nativeDisplay.display)
  const expectedContent = options.expected.map(block => block.type === 'text' ? block : { type: 'image', mimeType: 'image/png', data: readFileSync(block.path).toString('base64') })
  const expectedStructured = { nextCount: options.input.count + 1, enabled: options.input.enabled, text: options.input.text }
  expect(readMcpCallArguments(options.receiptLog)).toEqual([{ name: 'inspect', arguments: options.input }])
  const receipts: unknown = JSON.parse(readFileSync(options.receiptLog, 'utf8'))
  expect(deepseekHarnessInspectReply(receipts, options.input)).toEqual({ content: expectedContent, structuredContent: expectedStructured, _meta: { privateFixture: true } })
  expect(receipt.blocks.filter(block => block.type === 'image')).toHaveLength(options.retainedImages)
  expect(nativeDisplay.display.filter(block => block.type === 'image')).toHaveLength(options.expected.filter(block => block.type === 'image').length)
  expect(receipt.frame).not.toHaveProperty('_meta')
  await options.testInfo?.attach('deepseek-native-mcp-path-receipt', { body: JSON.stringify({ agentId: agent.id, sessionId: agent.agentSessionId, callId: options.callId, paths: receipt.paths, previewText: nativeDisplay.previewText, frame: receipt.frame, supplement: receipt.supplement }), contentType: 'application/json' })
  for (const reloaded of [false, true]) {
    if (reloaded) {
      await context.page.reload()
      await openWorkspace(context.page, context.workspaceId)
    }
    const snapshot = await readNativeMessageSnapshot(context, agent.id)
    const result = readDeepseekHarnessNativeOutput(snapshot, options.callId)
    expect(result.frame).toEqual(receipt.frame)
    expect(result.message.content).toEqual(receipt.message.content)
    expect(nativeMcpDisplay(snapshot, options.callId)).toEqual(nativeDisplay)
    const bubble = toolCallRow(context.page, options.callId)
    await expect(bubble).toHaveCount(1)
    await expect(bubble).toHaveAttribute('data-tool-status', 'completed')
    await expandNativeResultView(bubble)
    const pathList = bubble.getByTestId('tool-output-file-paths')
    await expect(pathList).toHaveCount(1)
    expect(await pathList.textContent()).toBe(receipt.paths.map(path => `Output file:${path}`).join(''))
    await expect(pathList.locator('a, button')).toHaveCount(0)
    expect(await readAttachedWithArgument(bubble, 'native output property order', nativeOutputPathsPrecedePreview, orderMarkers)).toBe(true)
    const images = bubble.locator('button[aria-label="Open image"]:visible img:visible')
    await expect(images).toHaveCount(options.expected.filter(block => block.type === 'image').length)
    // Compare the complete serialized occurrence list. A boolean keeps the large text out of the failure message.
    const last: { rendered: DeepseekHarnessRenderedMcpBlock[] | null } = { rendered: null }
    try {
      await expect.poll(async () => {
        last.rendered = await readAttachedWithArgument(bubble, 'the native MCP result order', deepseekHarnessRenderedMcpContent, [LIMITED_TEXT_DISPLAY_NOTICE, PLAIN_TEXT_DISPLAY_NOTICE])
        return last.rendered !== null && JSON.stringify(last.rendered) === expectedOrder
      }).toBe(true)
    }
    catch (error) {
      await options.testInfo?.attach('deepseek-native-mcp-order-mismatch', { body: JSON.stringify({ expected: nativeDisplay.display, rendered: last.rendered }), contentType: 'application/json' })
      throw error
    }
    const expectedImages = options.expected.flatMap(block => block.type === 'image' ? [readFileSync(block.path).toString('base64')] : [])
    for (let index = 0; index < expectedImages.length; index++) {
      const bytes = expectedImages[index]
      if (bytes === undefined)
        throw new Error('The native MCP image proof lost its exact expected bytes.')
      await expect.poll(() => readAttachedWithArgument(images.nth(index), 'the exact native MCP image', async (matches, expected) => {
        const element = matches.find((match): match is HTMLImageElement => match.isConnected && match instanceof HTMLImageElement)
        if (!element)
          return null
        if (!element.complete || element.naturalWidth !== 64 || element.naturalHeight !== 64)
          return false
        const image = new Image()
        image.src = `data:image/png;base64,${expected}`
        await image.decode()
        if (!element.isConnected)
          return null
        const pixels = (source: HTMLImageElement) => {
          const canvas = document.createElement('canvas')
          canvas.width = 64
          canvas.height = 64
          const drawing = canvas.getContext('2d')
          if (!drawing)
            throw new Error('The native MCP image proof requires a canvas context.')
          drawing.drawImage(source, 0, 0)
          return drawing.getImageData(0, 0, 64, 64).data
        }
        const actual = pixels(element)
        const wanted = pixels(image)
        return actual.length === wanted.length && actual.every((value, offset) => value === wanted[offset])
      }, bytes)).toBe(true)
    }
    await copyNativeToolOutputPreview(context.page, bubble, nativeDisplay.previewText)
  }
}
