import { readFileSync } from 'node:fs'
import { expect } from '@playwright/test'
import { antigravityTest } from '../antigravity-fixtures'
import { nativeMessageBody, readNativeMessageSnapshot } from '../helpers/nativeMessages'
import { currentNativeAgent } from '../helpers/nativeScenario'
import { readToolCall } from '../helpers/providerToolCalls'
import { expectDecodedImageInBubble, expectPngInRequest, imageInBubble, runToolImageTurn } from '../helpers/toolImages'
import { readAttached, toolCallRow } from '../helpers/ui'

antigravityTest('recovers the actual native image bytes into the exact tool result after reload', async ({ native, authenticatedAntigravityWorkspace }) => {
  const id = 'antigravity-read-native-image'
  const { fileName, path, resultRequest } = await runToolImageTurn(native, {
    workingDir: authenticatedAntigravityWorkspace.workingDir,
    marker: 'antigravity-native',
    toolCall: image => readToolCall(native.provider, id, image.path),
  })
  expectPngInRequest(resultRequest)
  const agent = await currentNativeAgent(native)
  const bytes = readFileSync(path).toString('base64')
  const result = toolCallRow(native.page, id)
  const proveImage = async () => {
    await expect(result).toHaveCount(1)
    await expect(toolCallRow(native.page, id, 'request')).toContainText(fileName)
    await expectDecodedImageInBubble(result)
    const image = imageInBubble(result)
    await expect(image).toHaveAttribute('src', `data:image/png;base64,${bytes}`)
    expect(await readAttached(image, 'the decoded native image dimensions', (elements) => {
      const attached = elements.find((element): element is HTMLImageElement => element.isConnected && element instanceof HTMLImageElement)
      return attached
        ? [attached.naturalWidth, attached.naturalHeight]
        : null
    })).toEqual([64, 64])
    const snapshot = await readNativeMessageSnapshot(native, agent.id)
    const records = snapshot.messages.filter(message => message.spanId === id).map(nativeMessageBody)
    expect(records).toHaveLength(2)
    expect(JSON.stringify(records[1])).toContain(bytes)
  }
  await proveImage()
  await native.page.reload()
  await proveImage()
})
