import { readFileSync } from 'node:fs'
import { expect } from '@playwright/test'
import { geminiResultImages } from '../../../src/components/chat/providers/gemini/extractors/results'
import { isObject } from '../../../src/lib/jsonPick'
import { geminiTest } from '../gemini-fixtures'
import { readNativeMessageSnapshot } from '../helpers/nativeMessages'
import { currentNativeAgent } from '../helpers/nativeScenario'
import { readToolCall } from '../helpers/providerToolCalls'
import { expectDecodedImageInBubble, expectPngInRequest, imageInBubble, runToolImageTurn } from '../helpers/toolImages'
import { readAttached, toolCallRow } from '../helpers/ui'
import { readGeminiStoredToolRecord } from './toolRecord'

geminiTest('recovers the actual native image bytes into the exact tool result after reload', async ({ native, authenticatedGeminiWorkspace }) => {
  const id = 'gemini-read-native-image'
  const { fileName, path, resultRequest } = await runToolImageTurn(native, {
    workingDir: authenticatedGeminiWorkspace.workingDir,
    marker: 'gemini-native',
    toolCall: image => readToolCall(native.provider, id, image.path),
  })
  // Gemini CLI gives the binary content of a read to the model as an inline data part beside its function response, so
  // the next model request holds the PNG.
  expectPngInRequest(resultRequest)
  const agent = await currentNativeAgent(native)
  const bytes = readFileSync(path).toString('base64')
  // Gemini prefixes its tool name to the call ID of its stored rows.
  const nativeCallId = `read_file__${id}`
  const request = toolCallRow(native.page, nativeCallId, 'request')
  const result = toolCallRow(native.page, nativeCallId)
  const proveImage = async () => {
    await expect(request).toHaveCount(1)
    await expect(request).toContainText(fileName)
    await expect(result).toHaveCount(1)
    await expect(result).toContainText('Binary content provided (1 item(s)).')
    await expectDecodedImageInBubble(result)
    const image = imageInBubble(result)
    await expect(image).toHaveAttribute('src', `data:image/png;base64,${bytes}`)
    expect(await readAttached(image, 'the decoded native image dimensions', (elements) => {
      const attached = elements.find((element): element is HTMLImageElement => element.isConnected && element instanceof HTMLImageElement)
      return attached ? [attached.naturalWidth, attached.naturalHeight] : null
    })).toEqual([64, 64])
    const snapshot = await readNativeMessageSnapshot(native, agent.id)
    const records = snapshot.messages.filter(message => message.spanId === nativeCallId).map(readGeminiStoredToolRecord).filter(isObject)
    expect(records).toHaveLength(1)
    expect(geminiResultImages(records[0]!)).toEqual([{ mimeType: 'image/png', data: bytes }])
  }
  await proveImage()
  await native.page.reload()
  await proveImage()
})
