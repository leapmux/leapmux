import type { JsonValue } from '@bufbuild/protobuf'
import type { TestInfo } from '@playwright/test'
import type { AgentChatMessage, AgentInfo } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import type { MockModelRequestRecord } from '../helpers/mockModelScript'
import type { NativeResumeEvidence, NativeResumeResult } from '../helpers/nativeLifecycle'
import type { ManagedNativeScenarioContext } from '../helpers/nativeScenario'
import { Buffer } from 'node:buffer'
import { readdirSync, writeFileSync } from 'node:fs'
import { join } from 'node:path'
import { toJson } from '@bufbuild/protobuf'
import { AgentChatMessageSchema, AgentInfoSchema } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { isObject } from '../../../src/lib/jsonPick'
import { exerciseSessionResume } from '../helpers/nativeLifecycle'
import { readAllAgentMessages } from '../helpers/nativeMessages'
import { nativeAgentById, selectedAgentTab } from '../helpers/nativeScenario'
import { readNativeToolOutputFile } from '../helpers/nativeToolOutputFile'
import { antigravityNativeSessionDir } from './nativeStore'

interface ArchiveObservation {
  path: string
  bytes: number
  attachment: string
}

interface AntigravityResumeReceipt {
  phase: 'stored' | 'opened' | 'continued' | 'failed'
  priorAgent: JsonValue | null
  selectedAgent: JsonValue | null
  messages: JsonValue[]
  archives: ArchiveObservation[]
  request: MockModelRequestRecord | null
  failure: string | null
}

type AntigravityResumeObservation = NativeResumeEvidence | {
  phase: 'failed'
  prior?: Readonly<AgentInfo>
  cause: unknown
}

function failureText(error: unknown): string {
  return error instanceof Error
    ? `${error.name}: ${error.message}`
    : String(error)
}

function validateTranscript(text: string): void {
  const lines = text.split('\n')
  for (const [index, line] of lines.entries()) {
    if (line.trim() === '')
      continue
    let record: unknown
    try {
      record = JSON.parse(line)
    }
    catch (error) {
      if (index === lines.length - 1 && !text.endsWith('\n'))
        return
      throw new Error('The native Antigravity transcript contains an invalid complete JSONL record.', { cause: error })
    }
    if (!isObject(record) || !Number.isSafeInteger(record.step_index) || Number(record.step_index) < 0 || typeof record.type !== 'string' || record.type === '')
      throw new Error('The native Antigravity transcript contains an invalid step.')
  }
}

/** Attach the observed Worker rows and the native transcript bytes of the exact cascade. */
export async function captureAntigravityResumeEvidence(
  context: ManagedNativeScenarioContext,
  testInfo: Pick<TestInfo, 'attach' | 'outputPath'>,
  evidence: AntigravityResumeObservation,
): Promise<void> {
  const prior = evidence.prior
  const receipt: AntigravityResumeReceipt = {
    phase: evidence.phase,
    priorAgent: prior
      ? toJson(AgentInfoSchema, prior, { alwaysEmitImplicit: true })
      : null,
    selectedAgent: null,
    messages: [],
    archives: [],
    request: evidence.phase === 'continued'
      ? evidence.request
      : null,
    failure: evidence.phase === 'failed'
      ? failureText(evidence.cause)
      : null,
  }
  const selectedTabId = await selectedAgentTab(context.page).getAttribute('data-tab-id')
  if (selectedTabId) {
    const selected = await nativeAgentById(context, selectedTabId)
    receipt.selectedAgent = selected
      ? toJson(AgentInfoSchema, selected, { alwaysEmitImplicit: true })
      : null
    if (selected) {
      const messages: AgentChatMessage[] = []
      await readAllAgentMessages(context, selected.id, page => messages.push(...page))
      receipt.messages = messages.map(message => toJson(AgentChatMessageSchema, message, { alwaysEmitImplicit: true }))
    }
  }
  if (prior) {
    const directory = join(antigravityNativeSessionDir(context, prior), '.system_generated', 'logs')
    const names = readdirSync(directory).filter(name => name === 'transcript.jsonl' || name === 'transcript_full.jsonl')
    if (names.length === 0)
      throw new Error('The native Antigravity session has no transcript to capture.')
    for (const name of names) {
      const path = join(directory, name)
      const text = readNativeToolOutputFile(path, 16 * 1024 * 1024)
      validateTranscript(text)
      const attachment = `antigravity-resume-${evidence.phase}-${name}`
      const output = testInfo.outputPath(attachment)
      writeFileSync(output, text, 'utf8')
      await testInfo.attach(attachment, { path: output, contentType: 'application/x-ndjson' })
      receipt.archives.push({
        path,
        bytes: Buffer.byteLength(text),
        attachment,
      })
    }
  }
  const attachment = `antigravity-resume-${evidence.phase}-receipt.json`
  const path = testInfo.outputPath(attachment)
  writeFileSync(path, JSON.stringify(receipt, null, 2), 'utf8')
  await testInfo.attach(attachment, { path, contentType: 'application/json' })
}

/** Reopen the native cascade and preserve evidence at each resume phase. */
export async function exerciseAntigravityResumeWithEvidence(
  context: ManagedNativeScenarioContext,
  testInfo: Pick<TestInfo, 'attach' | 'outputPath'>,
): Promise<NativeResumeResult> {
  let prior: Readonly<AgentInfo> | undefined
  try {
    return await exerciseSessionResume(context, {
      resumeEvidence: async (evidence) => {
        prior = evidence.prior
        await captureAntigravityResumeEvidence(context, testInfo, evidence)
      },
    })
  }
  catch (failure) {
    try {
      await captureAntigravityResumeEvidence(context, testInfo, {
        phase: 'failed',
        ...(prior
          ? { prior }
          : {}),
        cause: failure,
      })
    }
    catch (captureFailure) {
      throw new AggregateError([failure, captureFailure], 'The native resume scenario and its evidence capture failed.')
    }
    throw failure
  }
}
