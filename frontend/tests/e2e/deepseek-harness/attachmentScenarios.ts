import type { AttachmentKind } from '../helpers/attachments'
import type { ManagedNativeScenarioContext } from '../helpers/nativeScenario'
import { readFileSync } from 'node:fs'
import { basename } from 'node:path'
import { expect } from '@playwright/test'
import { nativeUserStrings } from '../helpers/attachmentModelProbe'
import { expectAttachmentOutcome, sendWithAttachment } from '../helpers/attachments'
import { assertPrivateNativePath } from '../helpers/nativePrivatePath'
import { runNativeToolTurn } from '../helpers/nativeToolExecution'
import { nativeToolResult, nativeToolResultContent } from '../helpers/nativeToolResult'
import { bashToolCall, readToolCall } from '../helpers/providerToolCalls'
import { getGlobalState } from '../helpers/server'
import { assistantBubbles, expectUserMessage } from '../helpers/ui'

/** Prove a native saved-path attachment through the tool that reads its complete bytes. */
export async function exerciseDeepseekHarnessFileAttachment(
  context: ManagedNativeScenarioContext,
  kind: Exclude<AttachmentKind, 'image'>,
  fileName: string,
): Promise<void> {
  const sourcePath = await expectAttachmentOutcome(context.page, kind, { supported: true, fileName })
  const source = readFileSync(sourcePath)
  const callId = `deepseek-attachment-${kind}`
  const tool = kind === 'text'
    ? readToolCall(context.provider, callId, '{{savedPath}}')
    : bashToolCall(context.provider, callId, `node -e 'process.stdout.write(require("node:fs").readFileSync(process.argv[1]).toString("base64"))' "{{savedPath}}"`)
  const { toolRequest: first, resultRequest: next } = await runNativeToolTurn(context, {
    toolCalls: [tool],
    captures: { savedPath: 'verbatim read-only copy saved at "([^"]+)"' },
    prompt: 'Read the attached file through its actual native saved path.',
    answer: 'The actual uploaded file bytes reached the model.',
    send: sendWithAttachment,
  })
  expect(first.protocol).toBe('anthropic-messages')
  expect(next.protocol).toBe('anthropic-messages')
  const handles = nativeUserStrings(first.body).join('\n')
  const savedPath = /verbatim read-only copy saved at "([^"]+)"/.exec(handles)?.[1]
  if (!savedPath)
    throw new Error('The native user input contains no saved attachment path.')
  assertPrivateNativePath(savedPath, getGlobalState().tmpDir)
  expect(basename(savedPath)).toBe(fileName)
  expect(readFileSync(savedPath)).toEqual(source)
  const result = nativeToolResult(next, callId)
  if (kind === 'text') {
    expect(source.toString('utf8')).not.toBe('')
    expect(result).toContain(source.toString('utf8'))
  }
  else {
    // The native Bash result adds a status marker only for a nonzero exit, a signal, a timeout, or a stop.
    // Exact text with no marker therefore proves the complete bytes and a clean exit.
    expect(nativeToolResultContent(next, callId)).toEqual([{ type: 'text', text: source.toString('base64') }])
  }
  await expectUserMessage(context.page, fileName)
  await expect(assistantBubbles(context.page).filter({ hasText: 'The actual uploaded file bytes reached the model.' }).first()).toBeVisible()
  await context.page.reload()
  await expectUserMessage(context.page, fileName)
}
