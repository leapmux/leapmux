import type { MockModelRequestRecord } from '../helpers/mockModelScript'
import type { NativeModeNotice } from '../helpers/nativeModeChange'
import type { ManagedNativeScenarioContext } from '../helpers/nativeScenario'
import { execFileSync } from 'node:child_process'
import { join } from 'node:path'
import { expect } from '@playwright/test'
import { isObject } from '../../../src/lib/jsonPick'
import { sqliteTextLiteral } from '../helpers/api'
import { MOCK_MODELS } from '../helpers/mockAgentEnvironment'
import { sendNativeAnswer } from '../helpers/nativeConversation'
import { chooseNativeModeWithNotice } from '../helpers/nativeModeChange'
import { assertPrivateNativePath } from '../helpers/nativePrivatePath'
import { currentNativeAgent, nativeModelInstructionText, nativeModelLastUserText } from '../helpers/nativeScenario'
import { exerciseNativeOption, exerciseRestoredNativeOption, expectSettingsOptionsOffered } from '../helpers/nativeSettings'
import { getGlobalState } from '../helpers/server'
import { expectSettingsChip, waitForNativeSettingsHydrated } from '../helpers/ui'
import { mimoTest } from '../mimo-fixtures'

interface NativeMessage {
  id: string
  info: Record<string, unknown>
  parts: Record<string, unknown>[]
}

/** Read the native primary-agent receipts from MiMo's private database. */
async function nativeMessages(context: ManagedNativeScenarioContext): Promise<NativeMessage[]> {
  const home = context.leapmuxServer.agentEnv?.MIMOCODE_HOME
  if (!home)
    throw new Error('The MiMo mode proof requires a private native home.')
  const database = join(home, 'data', 'mimocode.db')
  assertPrivateNativePath(database, getGlobalState().tmpDir)
  const agent = await currentNativeAgent(context)
  if (!agent.agentSessionId)
    throw new Error('The MiMo mode proof requires a native session.')
  // The Worker assembles text rows without this metadata. MiMo stores the resolved primary agent on its own reply.
  const sql = `SELECT m.id, m.data,
    COALESCE((SELECT json_group_array(json(p.data)) FROM part p WHERE p.message_id=m.id AND p.session_id=m.session_id), '[]') AS parts
    FROM message m WHERE m.session_id=${sqliteTextLiteral(agent.agentSessionId)}
    ORDER BY m.time_created, m.id`
  const rows: unknown = JSON.parse(execFileSync('sqlite3', ['-readonly', '-json', database, sql], { encoding: 'utf8' }) || '[]')
  if (!Array.isArray(rows))
    throw new TypeError('The native MiMo message query returned no array.')
  return rows.map((row: unknown) => {
    if (!isObject(row) || typeof row.id !== 'string' || typeof row.data !== 'string' || typeof row.parts !== 'string')
      throw new Error('The native MiMo message query returned an invalid record.')
    const info: unknown = JSON.parse(row.data)
    const parts: unknown = JSON.parse(row.parts)
    if (!isObject(info) || !Array.isArray(parts) || !parts.every(isObject))
      throw new Error('The native MiMo message contains invalid metadata or parts.')
    return { id: row.id, info, parts }
  })
}

mimoTest('applies the native primary agent mode before and after reload', async ({ native }) => {
  await exerciseNativeOption(native, {
    groupId: 'permissionMode',
    value: 'plan',
    nativeProof: (request) => {
      const instruction = nativeModelInstructionText(request)
      expect(instruction).toMatch(/Plan mode is (?:still )?active/i)
      expect(instruction).toMatch(/read-only/i)
      expect(request.body).toHaveProperty('model', MOCK_MODELS.zai)
      expect(request.body).toHaveProperty('reasoning_effort', 'high')
    },
  })
})

mimoTest('returns from Plan to Build and proves each native mode after reload', async ({ native }) => {
  await waitForNativeSettingsHydrated(native.page)
  await expectSettingsOptionsOffered(native.page, 'permissionMode', ['build', 'plan', 'compose'])
  await expectSettingsChip(native.page, 'Build')
  let observed = new Set((await nativeMessages(native)).map(message => message.id))
  const notices: NativeModeNotice[] = []
  for (const mode of [
    { value: 'plan', label: 'Plan', previousLabel: 'Build' },
    { value: 'build', label: 'Build', previousLabel: 'Plan' },
  ]) {
    const notice = await chooseNativeModeWithNotice(native, { groupId: 'permissionMode', ...mode, noticeLabel: 'Mode' })
    notices.push(notice)
    const nativeProof = async (request: MockModelRequestRecord) => {
      // A Build turn keeps earlier Plan reminders in its history. The newest user row states this turn's instructions.
      const instruction = nativeModelLastUserText(request)
      if (mode.value === 'plan') {
        expect(instruction).toMatch(/Plan mode is (?:still )?active/i)
        expect(instruction).toMatch(/read-only/i)
      }
      else {
        expect(instruction).not.toMatch(/Plan mode is (?:still )?active/i)
      }
      expect(request.body).toHaveProperty('model', MOCK_MODELS.zai)
      expect(request.body).toHaveProperty('reasoning_effort', 'high')
      const messages = await nativeMessages(native)
      const replies = messages.filter(message => !observed.has(message.id)
        && message.info.role === 'assistant' && (message.info.agentID ?? 'main') === 'main' && message.info.summary !== true)
      expect(replies, 'this turn creates one native reply from the primary agent').toHaveLength(1)
      const reply = replies[0]
      if (!reply)
        throw new Error('The current MiMo turn created no native reply.')
      expect(reply.info.agent).toBe(mode.value)
      expect(reply.info.mode).toBe(mode.value)
      expect(reply.info.error).toBeUndefined()
      const completed = isObject(reply.info.time)
        ? reply.info.time.completed
        : undefined
      expect(completed).toEqual(expect.any(Number))
      expect(completed).toBeGreaterThan(0)
      const parent = messages.find(message => message.id === reply.info.parentID)
      if (!parent)
        throw new Error('The current MiMo reply identifies no native user message.')
      expect(observed.has(parent.id), 'the reply belongs to the new user turn').toBe(false)
      expect(parent.info.role).toBe('user')
      const promptParts = parent.parts.filter(part => part.type === 'text' && part.synthetic !== true)
      expect(promptParts.length).toBeGreaterThan(0)
      for (const part of promptParts) {
        expect(part.text).toEqual(expect.any(String))
        if (typeof part.text !== 'string')
          throw new TypeError('The current MiMo user message contains no text.')
        expect(nativeModelLastUserText(request)).toContain(part.text)
      }
      observed = new Set(messages.map(message => message.id))
    }
    await nativeProof(await sendNativeAnswer(native, 'Reply once with the current native setting.', `MiMo used ${mode.label} mode.`))
    await exerciseRestoredNativeOption(native, { groupId: 'permissionMode', value: mode.value, nativeProof })
    await expectSettingsChip(native.page, mode.label)
    await notice.assertStored()
  }
  for (const notice of notices)
    await notice.assertStored()
})
