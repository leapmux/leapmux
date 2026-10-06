import { readFileSync } from 'node:fs'
import { join } from 'node:path'
import { expect } from '@playwright/test'
import { geminiResultImages } from '../../../src/components/chat/providers/gemini/extractors/results'
import { isObject } from '../../../src/lib/jsonPick'
import { geminiTest } from '../gemini-fixtures'
import { readNativeMessageSnapshot } from '../helpers/nativeMessages'
import { currentNativeAgent } from '../helpers/nativeScenario'
import { waitForNativeToolSteps } from '../helpers/nativeToolExecution'
import { readToolCall } from '../helpers/providerToolCalls'
import { expectDecodedImageInBubble, imageInBubble, writeToolImage } from '../helpers/toolImages'
import { readAttached, sendMessage } from '../helpers/ui'
import { nativeContext } from './scenarios'
import { readGeminiStoredToolRecord } from './toolRecord'

geminiTest('recovers the actual native image bytes into the exact tool result after reload', async ({ page, modelScript, leapmuxServer, authenticatedGeminiWorkspace }) => {
  const context = nativeContext({ page, modelScript, leapmuxServer, workspaceId: authenticatedGeminiWorkspace.workspaceId })
  const agent = await currentNativeAgent(context)
  const filename = writeToolImage(agent.workingDir, 'gemini-native')
  const bytes = readFileSync(join(agent.workingDir, filename)).toString('base64')
  const id = 'gemini-read-native-image'
  await modelScript.queue({ toolCalls: [readToolCall(context.provider, id, join(agent.workingDir, filename))] }, { text: 'The actual native image read completed.' })
  await sendMessage(page, modelScript.prompt('Read the actual image file.'))
  await waitForNativeToolSteps(context, 2)
  const request = page.locator(`[data-testid="message-bubble"][data-tool-call-id="read_file__${id}"][data-tool-row-role="request"]:visible`)
  const result = page.locator(`[data-testid="message-bubble"][data-tool-call-id="read_file__${id}"][data-tool-row-role="result"]:visible`)
  const proveImage = async () => {
    await expect(request).toHaveCount(1)
    await expect(request).toContainText(filename)
    await expect(result).toHaveCount(1)
    await expect(result).toContainText('Binary content provided (1 item(s)).')
    await expectDecodedImageInBubble(result)
    const image = imageInBubble(result)
    await expect(image).toHaveAttribute('src', `data:image/png;base64,${bytes}`)
    expect(await readAttached(image, 'the decoded native image dimensions', (elements) => {
      const attached = elements.find((element): element is HTMLImageElement => element.isConnected && element instanceof HTMLImageElement)
      return attached ? [attached.naturalWidth, attached.naturalHeight] : null
    })).toEqual([64, 64])
    const snapshot = await readNativeMessageSnapshot(context, agent.id)
    const records = snapshot.messages.filter(message => message.spanId === `read_file__${id}`).map(readGeminiStoredToolRecord).filter(isObject)
    expect(records).toHaveLength(1)
    expect(geminiResultImages(records[0]!)).toEqual([{ mimeType: 'image/png', data: bytes }])
  }
  await proveImage()
  await page.reload()
  await proveImage()
})
