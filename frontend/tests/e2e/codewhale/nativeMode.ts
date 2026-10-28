import type { MockModelRequestRecord } from '../helpers/mockModelScript'
import { existsSync, lstatSync, readdirSync, readFileSync, realpathSync } from 'node:fs'
import { isAbsolute, join } from 'node:path'
import { isObject } from '../../../src/lib/jsonPick'
import { assertPrivateNativePath } from '../helpers/nativePrivatePath'

export type CodewhaleNativeMode = 'agent' | 'plan' | 'operate'

/** Read the runtime's own thread state without writing or seeding a native store. */
export function readCodewhaleNativeMode(options: {
  home: string
  runDir: string
  threadId: string
  workingDir: string
}): { mode: CodewhaleNativeMode, path: string, record: Record<string, unknown> } {
  if (!isAbsolute(options.home) || !isAbsolute(options.runDir) || !isAbsolute(options.workingDir)
    || !options.threadId || options.threadId === '.' || options.threadId === '..' || /[/\\\0]/u.test(options.threadId)) {
    throw new Error('The native Codewhale mode proof requires absolute private directories and one thread ID.')
  }
  assertPrivateNativePath(options.home, options.runDir)
  assertPrivateNativePath(options.workingDir, options.runDir)
  const stores = join(options.home, 'leapmux')
  if (!existsSync(stores))
    throw new Error('The native Codewhale thread has no private store.')
  assertPrivateNativePath(stores, options.runDir)
  const matches: Array<{ mode: CodewhaleNativeMode, path: string, record: Record<string, unknown> }> = []
  for (const entry of readdirSync(stores, { withFileTypes: true })) {
    if (!entry.isDirectory() || !entry.name.startsWith('store-'))
      continue
    const path = join(stores, entry.name, 'runtime', 'threads', `${options.threadId}.json`)
    const info = lstatSync(path, { throwIfNoEntry: false })
    if (!info)
      continue
    assertPrivateNativePath(path, options.runDir)
    if (!info.isFile() || info.size <= 0 || info.size > 1024 * 1024)
      throw new Error('The native Codewhale thread record is not a regular file within one mebibyte.')
    const bytes = readFileSync(path)
    if (bytes.length !== info.size)
      throw new Error('The native Codewhale thread record changed during the read.')
    const record: unknown = JSON.parse(bytes.toString('utf8'))
    if (!isObject(record) || record.id !== options.threadId || typeof record.workspace !== 'string')
      throw new Error('The native Codewhale thread record has another owner.')
    assertPrivateNativePath(record.workspace, options.runDir)
    if (realpathSync.native(record.workspace) !== realpathSync.native(options.workingDir))
      throw new Error('The native Codewhale thread record has another workspace.')
    if (record.mode !== 'agent' && record.mode !== 'plan' && record.mode !== 'operate')
      throw new Error('The native Codewhale thread record has an unknown mode.')
    matches.push({ mode: record.mode, path, record })
  }
  if (matches.length !== 1)
    throw new Error('The native Codewhale mode proof requires exactly one matching thread record.')
  return matches[0]!
}

/** The native API adds this contract on entering Operate and retains it in history. */
export function hasCodewhaleOperateContract(request: MockModelRequestRecord): boolean {
  if (request.protocol !== 'openai-chat-completions' || !isObject(request.body) || !Array.isArray(request.body.messages))
    return false
  return request.body.messages.filter(isObject).some(message => message.role === 'user'
    && typeof message.content === 'string'
    && message.content.startsWith('<codewhale:runtime_event kind="operate_contract" visibility="internal">\n')
    && message.content.endsWith('</codewhale:runtime_event>\n<turn_meta>\nInput provenance: runtime (non-authoritative)\n</turn_meta>')
    && message.content.includes('Orchestrate by default:')
    && message.content.includes('`workflow`')
    && message.content.includes('parallel'))
}
