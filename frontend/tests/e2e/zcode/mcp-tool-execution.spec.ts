import { readFileSync } from 'node:fs'
import { join } from 'node:path'
import { expect } from '@playwright/test'
import { isObject } from '../../../src/lib/jsonPick'
import { zcodeNodeImageToolCall } from '../helpers/providerToolCalls'
import { expectToolRowImage, writeToolImage } from '../helpers/toolImages'
import { sendMessage, waitForAgentIdle } from '../helpers/ui'
import { zcodeTest } from '../zcode-fixtures'

zcodeTest('mcp-tool-execution: shows the picture emitted by the native Node tool', async ({ authenticatedZCodeWorkspace, page, modelScript }) => {
  const workingDir = authenticatedZCodeWorkspace.workingDir
  if (!workingDir)
    throw new Error('ZCode test workspace has no working directory')
  const fileName = writeToolImage(workingDir, 'zcode-native')
  const base64 = readFileSync(join(workingDir, fileName)).toString('base64')
  await modelScript.queue(
    { toolCalls: [zcodeNodeImageToolCall('zcode-node-image', base64, fileName)] },
    { text: `I inspected ${fileName}.` },
  )
  await sendMessage(page, modelScript.prompt(`Show the image result for ${fileName}.`))
  await modelScript.waitForSteps(1)
  const permission = page.getByTestId('control-banner').filter({ visible: true })
  await expect(permission).toContainText('mcp__node_repl__js')
  await page.getByTestId('control-allow-btn').click()
  const status = await modelScript.waitForSteps()
  const body = status.requests.find(request => request.stepIndex === 1)?.body
  const nativeImage = isObject(body) && Array.isArray(body.messages) && body.messages.some(message =>
    isObject(message) && message.role === 'user' && Array.isArray(message.content) && message.content.some(item =>
      isObject(item) && item.type === 'image_url' && isObject(item.image_url) && item.image_url.url === `data:image/png;base64,${base64}`,
    ),
  )
  expect(nativeImage).toBe(true)
  await waitForAgentIdle(page)
  await expectToolRowImage(page, fileName)
})
