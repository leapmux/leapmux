import type { Locator, Page } from '@playwright/test'
import type { MockModelRequestRecord, MockModelToolCall } from './mockModelScript'
import type { NativeScenarioContext } from './nativeScenario'
import { Buffer } from 'node:buffer'
import { writeFileSync } from 'node:fs'
import { join } from 'node:path'
import { expect } from '@playwright/test'
import { FINISHED_TOOL_STATUSES } from '../../../src/components/chat/model/toolCallStatus'
import { escapeRegExp } from '../../../src/lib/regexp'
import { nativeTextStep } from './nativeScenario'
import { runNativeToolTurn } from './nativeToolExecution'
import { answerControl, readAttached, sendMessage, toolRows, waitForAgentIdle, waitForControlBanner } from './ui'

/**
 * Image-in-tool-result fixtures and assertions.
 *
 * A provider's Read tool or a local Model Context Protocol tool returns image
 * content from a real PNG. The mock scripts the call. A decoded picture in the
 * correlated result bubble proves that the agent ran it and LeapMux drew it.
 */

/**
 * A 64x64 RGBA PNG: a teal field with a green marker square. Valid chunks and
 * CRCs, large enough that no decoder rejects it as a degenerate image.
 */
const TOOL_IMAGE_PNG_BASE64
  = 'iVBORw0KGgoAAAANSUhEUgAAAEAAAABACAYAAACqaXHeAAAAb0lEQVR42u3YMREAIAwEwZcYicjBFSigykwatjgDW15S63wdAAAAAAAAAODdTi8AAAAAAAAAAAAAAAAAAAAAgCECAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAJjqArCUycOeoJLSAAAAAElFTkSuQmCC'

/**
 * Write a PNG into `workingDir` and return its file name.
 *
 * The name carries a marker the prompt never states, so a tool row that names
 * it can only come from the tool call the script issued.
 */
export function writeToolImage(workingDir: string, marker: string): string {
  const name = `tool-image-${marker}.png`
  writeFileSync(join(workingDir, name), Buffer.from(TOOL_IMAGE_PNG_BASE64, 'base64'))
  return name
}

/**
 * The base64 text that starts every PNG. Its 11 characters hold 66 bits: the 64 bits of the PNG signature, and
 * the first 2 bits of the IHDR length that follows the signature, which are zero in every PNG.
 * A request that holds it carries PNG bytes in base64. It does not identify one PNG.
 */
export const PNG_BASE64_PREFIX = TOOL_IMAGE_PNG_BASE64.slice(0, 11)

/**
 * Require the PNG bytes of a tool result, in base64, anywhere in the model request that follows the tool step.
 * For a provider that gives the image as an image part, `expectImageDataUriInRequest` states the part and its format.
 * The assertion receives a boolean, so the large request body stays out of the failure message.
 */
export function expectPngInRequest(request: Pick<MockModelRequestRecord, 'body' | 'protocol'>): void {
  expect(JSON.stringify(request.body).includes(PNG_BASE64_PREFIX), `the ${request.protocol} model request carries the tool image as ${PNG_BASE64_PREFIX}`).toBe(true)
}

/** The media type of an image part that `expectImageDataUriInRequest` can identify by the signature of its bytes. */
export type ToolImageMediaType = 'image/png' | 'image/webp'

const PNG_SIGNATURE = Buffer.from([0x89, 0x50, 0x4E, 0x47, 0x0D, 0x0A, 0x1A, 0x0A])

/** Whether `bytes` start with the signature of `mediaType`: the PNG signature, or a RIFF chunk of the WEBP form. */
function hasImageSignature(bytes: Buffer, mediaType: ToolImageMediaType): boolean {
  if (mediaType === 'image/png')
    return bytes.subarray(0, PNG_SIGNATURE.length).equals(PNG_SIGNATURE)
  return bytes.toString('latin1', 0, 4) === 'RIFF' && bytes.toString('latin1', 8, 12) === 'WEBP'
}

/**
 * Require an image part of `mediaType` in the model request that follows the tool step: a `data:<mediaType>;base64,`
 * URI whose decoded bytes start with the signature of that format.
 * A provider that encodes the tool image again before it gives the image to the model states the format that it
 * writes, as Oh My Pi does with WebP.
 * The assertion receives a boolean, so the large request body stays out of the failure message.
 */
export function expectImageDataUriInRequest(request: Pick<MockModelRequestRecord, 'body' | 'protocol'>, mediaType: ToolImageMediaType): void {
  const dataUris = new RegExp(`data:${escapeRegExp(mediaType)};base64,([A-Za-z0-9+/]+=*)`, 'g')
  const found = [...JSON.stringify(request.body).matchAll(dataUris)]
    .some(match => hasImageSignature(Buffer.from(match[1] ?? '', 'base64'), mediaType))
  expect(found, `the ${request.protocol} model request carries the tool image as an image part of type ${mediaType}`).toBe(true)
}

/** A PNG that `writeToolImage` wrote: its file name, and its path in the working directory. */
export interface ToolImage {
  fileName: string
  path: string
}

/** One native turn whose tool reads a PNG. */
export interface ToolImageTurn {
  /** The directory that receives the PNG. The native agent must be able to read it. */
  workingDir: string
  /** The marker in the file name of the PNG. A tool row that shows the file name can only come from this turn. */
  marker: string
  /** Build the native tool call that reads the PNG. A provider passes the file name or the path, as its tool needs. */
  toolCall: (image: ToolImage) => MockModelToolCall
  /**
   * Require the permission banner that shows the file name of the PNG, and allow it.
   * Without it, the turn allows each native approval as `runNativeToolTurn` does, and checks no banner text.
   */
  approve?: boolean
}

/** The PNG of one {@link ToolImageTurn} and the model request that holds its tool result. */
export interface ToolImageTurnResult extends ToolImage {
  resultRequest: MockModelRequestRecord
}

/**
 * Write a PNG, run one native turn whose tool reads it, and return the model request after the tool step.
 * The answer goes through `nativeTextStep`, so a provider that answers through a tool keeps its own form.
 * The caller checks what the provider draws: `expectToolRowImage` or `expectToolRowWithoutImage`.
 */
export async function runToolImageTurn(context: NativeScenarioContext, turn: ToolImageTurn): Promise<ToolImageTurnResult> {
  if (!turn.workingDir)
    throw new Error('The tool image turn needs the working directory of the native agent.')
  const fileName = writeToolImage(turn.workingDir, turn.marker)
  const image: ToolImage = { fileName, path: join(turn.workingDir, fileName) }
  const call = turn.toolCall(image)
  const prompt = `Read ${fileName} and describe it.`
  const answer = `I inspected ${fileName}.`
  if (!turn.approve) {
    const { resultRequest } = await runNativeToolTurn(context, { toolCalls: [call], prompt, answer })
    return { ...image, resultRequest }
  }
  const start = await context.modelScript.queue({ toolCalls: [call] }, nativeTextStep(context, answer))
  await sendMessage(context.page, context.modelScript.prompt(prompt))
  await context.modelScript.waitForSteps(start + 1)
  await expect(await waitForControlBanner(context.page)).toContainText(fileName)
  await answerControl(context.page, 'allow')
  await context.modelScript.waitForSteps(start + 2)
  await waitForAgentIdle(context.page)
  return { ...image, resultRequest: await context.modelScript.requestAt(start + 1) }
}

/** Check the picture inside one correlated result bubble. */
async function expectDecodedImage(image: Locator): Promise<void> {
  await expect(image).toBeVisible()
  await expect.poll(() => readAttached(image, 'decoded tool image', (matches) => {
    const attached = matches.find((element): element is HTMLImageElement => element.isConnected && element instanceof HTMLImageElement)
    return attached ? attached.naturalWidth : null
  })).toBeGreaterThan(0)
}

/** The visible picture inside one selected result bubble. */
export function imageInBubble(bubble: Locator): Locator {
  return bubble.locator('button[aria-label="Open image"]:visible img:visible').first()
}

/** Decode the picture inside one selected result bubble. */
export async function expectDecodedImageInBubble(bubble: Locator): Promise<void> {
  await expectDecodedImage(imageInBubble(bubble))
}

/** Find the visible named row and its provider-neutral tool-call identity. */
async function namedToolCall(page: Page, fileName: string): Promise<{ bubble: Locator, chatContainer: Locator, callID: string, role: string, sequence: bigint, status: string }> {
  const namedRow = toolRows(page).filter({ hasText: fileName }).last()
  await expect(namedRow).toBeVisible()
  const matchedBubble = namedRow.locator('xpath=ancestor::*[@data-testid="message-bubble"][1]')
  const identity = await readAttached(matchedBubble, 'the named tool bubble', (matches) => {
    const attached = matches.find(bubble => bubble.isConnected)
    if (!attached)
      return null
    return {
      callID: attached.getAttribute('data-tool-call-id'),
      role: attached.getAttribute('data-tool-row-role'),
      sequenceText: attached.getAttribute('data-message-seq'),
      status: attached.getAttribute('data-tool-status'),
      chatInstanceID: attached.closest('[data-testid="chat-container"]')?.getAttribute('data-chat-instance-id'),
    }
  })
  const callID = identity.callID
  if (!callID)
    throw new Error('the named tool row has no call ID')
  const sequenceText = identity.sequenceText
  if (!sequenceText || !/^\d+$/.test(sequenceText))
    throw new Error('the named tool row has no valid message sequence')
  const role = identity.role
  if (!role)
    throw new Error('the named tool row has no row role')
  const chatInstanceID = identity.chatInstanceID
  if (!chatInstanceID)
    throw new Error('the named tool row has no chat instance ID')
  const selector = await callSelector(page, callID)
  const chatContainer = await chatContainerForID(page, chatInstanceID)
  const bubble = chatContainer.locator(`[data-chat-scroll-container="true"]:visible [data-testid="message-bubble"]:visible${selector}[data-message-seq="${sequenceText}"][data-tool-row-role="${role}"]`).last()
  return { bubble, chatContainer, callID, role, sequence: BigInt(sequenceText), status: identity.status ?? '' }
}

/** Use the browser's CSS parser to escape a native call ID for an attribute selector. */
async function callSelector(page: Page, callID: string): Promise<string> {
  const escapedID = await page.evaluate(id => CSS.escape(id), callID)
  return `[data-tool-call-id=${escapedID}]`
}

async function chatContainerForID(page: Page, chatInstanceID: string): Promise<Locator> {
  const escapedID = await page.evaluate(id => CSS.escape(id), chatInstanceID)
  return page.locator(`[data-testid="chat-container"][data-chat-instance-id=${escapedID}]`)
}

interface CallBubble {
  sequence: bigint
  role: string | null
}

/** Read attached same-ID rows from the visible chat for call pairing. */
async function readCallBubbles(chatContainer: Locator, selector: string): Promise<CallBubble[]> {
  const bubbles = chatContainer.locator(`[data-chat-scroll-container="true"]:visible [data-testid="message-bubble"]${selector}`)
  const raw = await readAttached<Array<{ sequence: string | null, role: string | null }>>(bubbles, 'tool-call bubbles', (matches) => {
    if (matches.length === 0)
      return []
    const attached = matches.filter(bubble => bubble.isConnected)
    if (attached.length === 0)
      return null
    return attached.map(bubble => ({
      sequence: bubble.getAttribute('data-message-seq'),
      role: bubble.getAttribute('data-tool-row-role'),
    }))
  })
  return raw.map(({ sequence, role }) => {
    if (!sequence || !/^\d+$/.test(sequence))
      throw new Error('a tool bubble has no valid message sequence')
    return { sequence: BigInt(sequence), role }
  })
}

/** Keep rows of one call between its request and the next reuse of its ID. */
async function callSegment(page: Page, chatContainer: Locator, callID: string, requestSequence: bigint): Promise<{ selector: string, rows: CallBubble[] }> {
  const selector = await callSelector(page, callID)
  const rows = await readCallBubbles(chatContainer, selector)
  const nextRequest = rows
    .filter(row => row.role === 'request' && row.sequence > requestSequence)
    .reduce<bigint | undefined>((next, row) => next === undefined || row.sequence < next ? row.sequence : next, undefined)
  return {
    selector,
    rows: rows.filter(row => row.sequence >= requestSequence && (nextRequest === undefined || row.sequence < nextRequest)),
  }
}

function firstResultSequence(rows: readonly CallBubble[], requestSequence: bigint): bigint | undefined {
  return rows
    .filter(row => row.role === 'result' && row.sequence > requestSequence)
    .reduce<bigint | undefined>((first, row) => first === undefined || row.sequence < first ? row.sequence : first, undefined)
}

const FINISHED_TOOL_STATUS_SET: ReadonlySet<string> = new Set(FINISHED_TOOL_STATUSES)

function noImage(page: Page): Locator {
  return page.locator('button[aria-label="Open image"]:not(*)')
}

/** Return one visible chat with matching rows, or reject an ambiguous call ID. */
async function uniqueMatchingChat(page: Page, matchingRows: Locator): Promise<Locator | null> {
  const rows = await readAttached(matchingRows, 'MCP call chat', (matches) => {
    if (matches.length === 0)
      return []
    const attached = matches.filter(bubble => bubble.isConnected)
    if (attached.length === 0)
      return null
    return attached.map(bubble => bubble.closest('[data-testid="chat-container"]')?.getAttribute('data-chat-instance-id') ?? '')
  })
  const chatIDs = [...new Set(rows)]
  if (chatIDs.includes(''))
    throw new Error('an MCP call row has no chat instance ID')
  if (chatIDs.length > 1)
    throw new Error('the MCP call ID matches rows in multiple visible chat tiles')
  return chatIDs[0] ? chatContainerForID(page, chatIDs[0]) : null
}

/** The image in a named tool call, including its paired split result. */
export async function toolResultImageForName(page: Page, fileName: string): Promise<Locator> {
  const { bubble, chatContainer, callID, role, sequence } = await namedToolCall(page, fileName)
  if (role !== 'request')
    return imageInBubble(bubble)
  const { selector, rows } = await callSegment(page, chatContainer, callID, sequence)
  const resultSequence = firstResultSequence(rows, sequence)
  if (resultSequence === undefined)
    return noImage(page)
  const resultBubble = chatContainer.locator(`[data-chat-scroll-container="true"]:visible [data-testid="message-bubble"]:visible${selector}[data-message-seq="${resultSequence}"][data-tool-row-role="result"]`).first()
  return imageInBubble(resultBubble)
}

/** Every image of one named call, including its hidden premeasure copies. */
export async function imagesForToolCall(page: Page, fileName: string): Promise<Locator> {
  const { chatContainer, callID, role, sequence } = await namedToolCall(page, fileName)
  const { selector, rows } = await callSegment(page, chatContainer, callID, sequence)
  const resultSequence = role === 'request' ? firstResultSequence(rows, sequence) : sequence
  const sequences = [...new Set(rows
    .filter(row => row.sequence >= sequence && (resultSequence === undefined || row.sequence <= resultSequence))
    .map(row => String(row.sequence)))]
  if (sequences.length === 0)
    throw new Error('the named tool row left the active chat before its images were checked')
  const visibleSelectors = sequences.map(rowSequence => `[data-chat-scroll-container="true"]:visible [data-testid="message-bubble"]${selector}[data-message-seq="${rowSequence}"] button[aria-label="Open image"] img`)
  const hiddenSelectors = sequences.map(rowSequence => `[data-chat-premeasure-root="true"] [data-testid="message-bubble"]${selector}[data-message-seq="${rowSequence}"] button[aria-label="Open image"] img`)
  return chatContainer.locator(visibleSelectors.join(', ')).or(chatContainer.locator(hiddenSelectors.join(', ')))
}

/** A finished call has a final row; split calls also need their paired result. */
export async function isNamedToolResultFinished(page: Page, fileName: string): Promise<boolean> {
  const { chatContainer, callID, role, sequence, status } = await namedToolCall(page, fileName)
  if (role !== 'request')
    return FINISHED_TOOL_STATUS_SET.has(status)
  const { selector, rows } = await callSegment(page, chatContainer, callID, sequence)
  const resultSequence = firstResultSequence(rows, sequence)
  if (resultSequence === undefined)
    return false
  const resultBubble = chatContainer.locator(`[data-chat-scroll-container="true"]:visible [data-testid="message-bubble"]:visible${selector}[data-message-seq="${resultSequence}"][data-tool-row-role="result"]`).first()
  const resultStatus = await readAttached(resultBubble, 'the paired tool result', (matches) => {
    if (matches.length === 0)
      return ''
    const attached = matches.find(bubble => bubble.isConnected)
    return attached ? attached.getAttribute('data-tool-status') ?? '' : null
  })
  return FINISHED_TOOL_STATUS_SET.has(resultStatus)
}

/** The image inside the exact Model Context Protocol call's result bubble. */
export async function mcpResultImage(page: Page, fileName: string, callID: string): Promise<Locator> {
  const selector = await callSelector(page, callID)
  const bubbleSelector = `[data-chat-scroll-container="true"]:visible [data-testid="message-bubble"]:visible${selector}`
  const startRows = page.locator(`${bubbleSelector}:is([data-tool-row-role="request"], [data-tool-row-role="update"])`)
  const resultRows = page.locator(bubbleSelector)
    .filter({ has: page.getByText(`MCP image ${fileName}`, { exact: true }) })
  const chatContainer = await uniqueMatchingChat(page, startRows) ?? await uniqueMatchingChat(page, resultRows)
  if (!chatContainer)
    return noImage(page)
  const resultBubble = chatContainer.locator(bubbleSelector)
    .filter({ has: page.getByText(`MCP image ${fileName}`, { exact: true }) })
    .last()
  const snapshot = await readAttached(resultBubble, 'the MCP image result', (matches, scrollerSelector) => {
    if (matches.length === 0)
      return { found: false, sequenceText: null, latestStartText: null }
    const attached = matches.find(bubble => bubble.isConnected)
    if (!attached)
      return null
    const scroller = attached.closest(scrollerSelector)
    const id = attached.getAttribute('data-tool-call-id')
    if (!scroller || !id)
      return { found: true, sequenceText: attached.getAttribute('data-message-seq'), latestStartText: null }
    const sameCall = [...scroller.querySelectorAll(`[data-testid="message-bubble"][data-tool-call-id=${CSS.escape(id)}]`)]
    const requests = sameCall.filter(bubble => bubble.isConnected && bubble.getAttribute('data-tool-row-role') === 'request')
    const starts = requests.length > 0
      ? requests
      : sameCall.filter(bubble => bubble.isConnected && bubble.getAttribute('data-tool-row-role') === 'update')
    let latestStart: bigint | null = null
    for (const bubble of starts) {
      const sequence = bubble.getAttribute('data-message-seq')
      if (!sequence || !/^\d+$/.test(sequence))
        continue
      const value = BigInt(sequence)
      if (latestStart === null || value > latestStart)
        latestStart = value
    }
    return {
      found: true,
      sequenceText: attached.getAttribute('data-message-seq'),
      latestStartText: latestStart === null ? null : String(latestStart),
    }
  })
  if (!snapshot.found)
    return noImage(page)
  const sequenceText = snapshot.sequenceText
  if (!sequenceText || !/^\d+$/.test(sequenceText))
    throw new Error('the MCP result bubble has no valid message sequence')
  if (snapshot.latestStartText !== null && BigInt(sequenceText) < BigInt(snapshot.latestStartText))
    return noImage(page)
  const stableResult = chatContainer.locator(`${bubbleSelector}[data-message-seq="${sequenceText}"]`)
    .filter({ has: page.getByText(`MCP image ${fileName}`, { exact: true }) })
    .last()
  return imageInBubble(stableResult)
}

/**
 * Assert a tool call of `fileName` drew an inline image.
 *
 * Two row layouts exist. A provider that merges the call and its result into one
 * row draws the picture inside the row that shows the file name. A provider that
 * keeps a separate result row shows the file name in the request row. It draws the
 * picture in the result row, which drops its header -- and with it the
 * `data-tool-message` hook. A text placeholder has no image element, so the file
 * name alone cannot satisfy this check.
 */
export async function expectToolRowImage(page: Page, fileName: string): Promise<void> {
  // An `img` element alone proves nothing: a broken picture keeps the element
  // and draws a placeholder. A decoded image reports its pixel width.
  await expect.poll(async () => (await toolResultImageForName(page, fileName)).count()).toBeGreaterThan(0)
  await expectDecodedImage(await toolResultImageForName(page, fileName))
}

/** Assert that a Model Context Protocol result shows the file name of the PNG and draws the PNG. */
export async function expectMcpToolImage(page: Page, fileName: string, callID: string): Promise<void> {
  await expect.poll(async () => (await mcpResultImage(page, fileName, callID)).count()).toBeGreaterThan(0)
  await expectDecodedImage(await mcpResultImage(page, fileName, callID))
}

/**
 * Assert a tool call of `fileName` ran and drew no picture.
 *
 * Codewhale and Cline build tool results as text only (matrix note 3). A
 * provider that restores a picture from another store may also draw none on
 * this path. The name alone proves the tool ran.
 */
export async function expectToolRowWithoutImage(page: Page, fileName: string): Promise<void> {
  await expect.poll(() => isNamedToolResultFinished(page, fileName)).toBe(true)
  await expect(await imagesForToolCall(page, fileName)).toHaveCount(0)
}
