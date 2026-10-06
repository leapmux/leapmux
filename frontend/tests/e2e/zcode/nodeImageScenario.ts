import type { MockModelRequestRecord } from '../helpers/mockModelScript'
import type { ManagedNativeScenarioContext } from '../helpers/nativeScenario'
import { readFileSync } from 'node:fs'
import { join } from 'node:path'
import { expect } from '@playwright/test'
import { isObject } from '../../../src/lib/jsonPick'
import { nativeTextStep } from '../helpers/nativeScenario'
import { zcodeNodeImageToolCall } from '../helpers/providerToolCalls'
import { expectToolRowImage, writeToolImage } from '../helpers/toolImages'
import { answerControl, sendMessage, waitForAgentIdle, waitForControlBanner } from '../helpers/ui'

/**
 * Read the URL of each image part of each user message of a Chat Completions request.
 * ZCode hands the picture of a tool to its model as an `image_url` part of a user message, not inside the tool result.
 */
export function zcodeUserImageUrls(request: Pick<MockModelRequestRecord, 'body'>): string[] {
  const messages = isObject(request.body) && Array.isArray(request.body.messages) ? request.body.messages : []
  return messages.flatMap((message) => {
    if (!isObject(message) || message.role !== 'user' || !Array.isArray(message.content))
      return []
    return message.content.flatMap(item => isObject(item) && item.type === 'image_url' && isObject(item.image_url) && typeof item.image_url.url === 'string' ? [item.image_url.url] : [])
  })
}

/**
 * Run the native Node tool of ZCode on a picture in `workingDir`, and allow its request.
 * Then require the exact picture in the next model request and in the tool row.
 * ZCode asks before the Node tool runs, and its banner states the tool.
 */
export async function exerciseZCodeNodeImage(context: ManagedNativeScenarioContext, workingDir: string): Promise<void> {
  const { page, modelScript } = context
  const fileName = writeToolImage(workingDir, 'zcode-native')
  const base64 = readFileSync(join(workingDir, fileName)).toString('base64')
  const call = zcodeNodeImageToolCall('zcode-node-image', base64, fileName)
  const start = await modelScript.queue({ toolCalls: [call] }, nativeTextStep(context, `I inspected ${fileName}.`))
  await sendMessage(page, modelScript.prompt(`Show the image result for ${fileName}.`))
  await modelScript.waitForSteps(start + 1)
  await expect(await waitForControlBanner(page)).toContainText(call.name)
  await answerControl(page, 'allow')
  await modelScript.waitForSteps(start + 2)
  expect(zcodeUserImageUrls(await modelScript.requestAt(start + 1))).toContain(`data:image/png;base64,${base64}`)
  await waitForAgentIdle(page)
  await expectToolRowImage(page, fileName)
}
