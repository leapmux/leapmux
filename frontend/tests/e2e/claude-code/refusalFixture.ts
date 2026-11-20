import { writeFileSync } from 'node:fs'
import { join } from 'node:path'
import process from 'node:process'
import { CLAUDE_SYSTEM_SUBTYPE } from '../../../src/generated/contracts/claude-protocol'
import { writeNodeLauncher } from '../helpers/nodeLauncher'
import { REFUSAL_CAPTURE, REFUSAL_CAPTURE_VERSION } from './refusalCapture'

export const REFUSAL_PROMPT = 'Start the refusal transcript fixture.'
export const REFUSED_TEXT = ['Refused partial one.', 'Refused partial two.'] as const
export const REFUSED_TOOL_CALL = 'toolu_refused'
export const REFUSAL_NOTICE = 'The local fallback will retry this response.'
export const REPLACEMENT_TEXT = 'Replacement answer from the fallback.'

/** Install a stream-JSON producer whose eviction frames wait for two browser views. */
export function prepareRefusalFixture(directory: string): { release: () => void } {
  const releasePath = join(directory, 'release-eviction')
  const script = join(directory, 'claude-refusal.cjs')
  const fixture = {
    directory,
    releasePath,
    prompt: REFUSAL_PROMPT,
    text: REFUSED_TEXT,
    toolId: REFUSED_TOOL_CALL,
    version: REFUSAL_CAPTURE_VERSION,
    captured: REFUSAL_CAPTURE,
    fallbackSubtype: CLAUDE_SYSTEM_SUBTYPE.ModelRefusalFallback,
    notice: REFUSAL_NOTICE,
    replacement: REPLACEMENT_TEXT,
  }
  // The fixture replays the ordinary init, assistant, tool and result envelopes.
  // The fallback and supersedes fields reproduce the native eviction signals.
  writeFileSync(script, `
const { randomUUID } = require('node:crypto')
const { existsSync, watch } = require('node:fs')
const { createInterface } = require('node:readline')
const fixture = ${JSON.stringify(fixture)}
if (process.argv.includes('--version') || process.argv.includes('-v')) {
  console.log(fixture.version + ' (Claude Code)')
  process.exit(0)
}
if (!process.argv.includes('stream-json'))
  throw new Error('The refusal fixture requires the Claude stream-JSON invocation.')
const session_id = randomUUID()
const write = frame => process.stdout.write(JSON.stringify(frame) + '\\n')
const captured = fixture.captured
const writeConversation = (frame, uuid = randomUUID()) => write({ ...frame, session_id, uuid })
const writeMessage = (frame, message, uuid = randomUUID()) => writeConversation({
  ...frame,
  message: { ...frame.message, ...message },
  timestamp: new Date().toISOString(),
}, uuid)
const writeText = (frame, uuid, messageId, text, extra = {}) => writeMessage({ ...frame, ...extra }, {
  id: messageId,
  content: [{ ...frame.message.content[0], text }],
}, uuid)
let started = false
let mode = captured.controls.initialize.response.response.current_permission_mode
const partials = [randomUUID(), randomUUID()]
const refusedResult = randomUUID()

function startTurn() {
  writeConversation(captured.running)
  writeConversation({ ...captured.init, cwd: process.cwd(), original_cwd: process.cwd(), permissionMode: mode })
  const firstMessageId = 'msg_' + randomUUID()
  writeText(captured.opening, partials[0], firstMessageId, fixture.text[0])
  writeMessage(captured.tool, {
    id: firstMessageId,
    content: [{ ...captured.tool.message.content[0], id: fixture.toolId }],
  })
  writeMessage({
    ...captured.toolResult,
    tool_result_meta: captured.toolResult.tool_result_meta.map(entry => ({ ...entry, id: fixture.toolId })),
  }, {
    content: [{ ...captured.toolResult.message.content[0], tool_use_id: fixture.toolId }],
  }, refusedResult)
  writeText(captured.answer, partials[1], 'msg_' + randomUUID(), fixture.text[1])
  let released = false
  const release = () => {
    if (released || !existsSync(fixture.releasePath))
      return
    released = true
    watcher.close()
    writeConversation({
      type: 'system', subtype: fixture.fallbackSubtype,
      trigger: 'refusal', direction: 'retry', scope: 'local',
      original_model: 'claude-opus-5-5', fallback_model: 'claude-sonnet-5-5',
      content: fixture.notice, retracted_message_uuids: [partials[0], refusedResult],
    })
    writeText(captured.answer, randomUUID(), 'msg_' + randomUUID(), fixture.replacement, { supersedes: partials[1] })
    writeConversation({ ...captured.result, result: fixture.replacement })
    writeConversation(captured.idle)
  }
  const watcher = watch(fixture.directory, release)
  release()
}

createInterface({ input: process.stdin }).on('line', line => {
  const frame = JSON.parse(line)
  if (frame.type === 'control_request') {
    const request = frame.request
    const response = structuredClone(captured.controls[request.subtype])
    if (!response)
      throw new Error('The refusal fixture received an unexpected control request: ' + request.subtype)
    response.response.request_id = frame.request_id
    if (request.subtype === 'initialize')
      response.response.response.pid = process.pid
    if (request.subtype === 'set_permission_mode') {
      mode = request.mode
      response.response.response.mode = mode
    }
    write(response)
  }
  else if (frame.type === 'user') {
    if (started || !JSON.stringify(frame.message).includes(fixture.prompt))
      throw new Error('The refusal fixture received an unexpected prompt.')
    started = true
    startTurn()
  }
})
`, { mode: 0o600 })
  writeNodeLauncher(directory, 'claude', { node: process.execPath, script })
  return {
    release: () => { writeFileSync(releasePath, '', { mode: 0o600 }) },
  }
}
