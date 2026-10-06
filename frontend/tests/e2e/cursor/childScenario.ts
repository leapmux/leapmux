import type { ManagedNativeScenarioContext } from '../helpers/nativeScenario'
import type { RunningNativeChild } from '../helpers/unsupportedSubagent'
import { writeFileSync } from 'node:fs'
import { join } from 'node:path'
import { currentNativeAgent } from '../helpers/nativeScenario'
import { readToolCall, spawnSubagentToolCall } from '../helpers/providerToolCalls'
import { openRunningNativeChild } from '../helpers/runningChildProof'
import { uniqueMarker } from '../helpers/shellArguments'

export interface CursorRunningChild extends RunningNativeChild {
  prompt: string
  marker: string
  answer: string
  filePath: string
}

/** Hold an actual local child after its real Read result reaches its own transcript. */
export async function openCursorRunningChild(
  context: ManagedNativeScenarioContext,
  options: { description?: string, allowExistingRows?: boolean } = {},
): Promise<CursorRunningChild> {
  const parent = await currentNativeAgent(context)
  if (!parent.workingDir)
    throw new Error('The actual Cursor child requires a private working directory.')
  const suffix = uniqueMarker()
  const gate = `cursor-native-child-${suffix}`
  const prompt = `NATIVECURSORCHILD${suffix}: read the supplied file and report once.`
  const marker = `CURSORCHILDREAD${suffix}`
  const answer = `CURSORCHILDFINAL${suffix}`
  const filePath = join(parent.workingDir, `native-child-${suffix}.txt`)
  writeFileSync(filePath, `${marker}\n`)
  const description = options.description ?? `Read the actual child file ${suffix}`
  const child = await openRunningNativeChild(context, {
    spawn: spawnSubagentToolCall(context.provider, `cursor-native-task-${suffix}`, {
      description,
      prompt: context.modelScript.prompt(prompt),
      nativeExecution: { modelId: 'mock-grok' },
    }),
    gate,
    singleRequest: true,
    ...(options.allowExistingRows === undefined ? {} : { allowExistingRows: options.allowExistingRows }),
    rowText: description,
    rules: [{
      name: 'the actual Cursor child reads its private file',
      when: { user: `^NATIVECURSORCHILD${suffix}:` },
      respond: {
        toolCalls: [readToolCall(context.provider, `cursor-native-read-${suffix}`, filePath)],
        text: answer,
        stream: { chunkChars: 1, delayMs: 0, gates: [{ afterChunk: 1, name: gate }] },
      },
      once: true,
    }],
  })
  return { ...child, prompt, marker, answer, filePath }
}
