import type { Locator, Page } from '@playwright/test'
import { Buffer } from 'node:buffer'
import { writeFileSync } from 'node:fs'
import { join } from 'node:path'
import { expect } from '@playwright/test'
import { FINISHED_TOOL_STATUSES } from '../../../src/components/chat/model/toolCallStatus'
import { readAttached, toolRows } from './ui'

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
 * row draws the picture inside the row that names the file. A provider that keeps
 * a separate result row names the file in the request row. It draws the picture
 * in the result row, which drops its header -- and with it the
 * `data-tool-message` hook. A text placeholder has no image element, so the file
 * name alone cannot satisfy this check.
 */
export async function expectToolRowImage(page: Page, fileName: string): Promise<void> {
  // An `img` element alone proves nothing: a broken picture keeps the element
  // and draws a placeholder. A decoded image reports its pixel width.
  await expect.poll(async () => (await toolResultImageForName(page, fileName)).count()).toBeGreaterThan(0)
  await expectDecodedImage(await toolResultImageForName(page, fileName))
}

/** Assert a Model Context Protocol result names the PNG and draws it. */
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
