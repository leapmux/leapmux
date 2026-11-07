import { isObject } from '../../../src/lib/jsonPick'
import { nativeTextBlocks } from '../helpers/nativeScenario'

const COMPRESSED_METADATA = '<turn_meta_unchanged />'
const HANDOFF_KINDS = new Set([
  'subagent_failed',
  'subagent_completion',
  'child_subagent_completion',
  'waiting_for_subagents',
])
const RESTORED_HEADERS = new Set([
  '[Codewhale restored sub-agent checkpoint]',
  '[Codewhale restored sub-agent checkpoints]',
  '[Codewhale restored sub-agent runtime checkpoint]',
  '[Codewhale restored Agent topology checkpoint]',
])
const RESTORED_METADATA = '<turn_meta>\nInput provenance: subagent_handoff (non-authoritative)\nRestore projection: subagent_checkpoint_v1\n</turn_meta>'
const TOPOLOGY_METADATA = '<turn_meta>\nInput provenance: runtime (non-authoritative)\nRuntime state: agent_topology_v1 (authoritative)\n</turn_meta>'

/** Read a complete current internal event with its kind-specific native provenance. */
export function readCodewhaleRuntimeEvent(content: unknown): { kind: string, text: string } | undefined {
  if (typeof content !== 'string')
    return undefined
  const match = /^<codewhale:runtime_event kind="([a-z_]+)"( priority="high")? visibility="internal">\n([\s\S]*)\n<\/codewhale:runtime_event>\n([\s\S]+)$/u.exec(content)
  if (!match)
    return undefined
  const kind = match[1]!
  if ((kind === 'subagent_failed') !== Boolean(match[2]))
    return undefined
  const provenance = HANDOFF_KINDS.has(kind)
    ? 'subagent_handoff'
    : kind === 'background_shell_completion'
      ? 'shell_completion'
      : 'runtime'
  const metadata = `<turn_meta>\nInput provenance: ${provenance} (non-authoritative)\n</turn_meta>`
  if (match[4] !== metadata)
    return undefined
  return { kind, text: match[3]! }
}

/** Separate native runtime context from user conversation, including restored checkpoints. */
export function isCodewhaleRuntimeContext(content: unknown): boolean {
  if (readCodewhaleRuntimeEvent(content))
    return true
  if (typeof content !== 'string')
    return false
  const topology = /^<codewhale:runtime_state kind="agent_topology" schema="v1" visibility="internal">\n[\s\S]*\n<\/codewhale:runtime_state>\n([\s\S]+)$/u.exec(content)
  if (topology && topology[1] === TOPOLOGY_METADATA)
    return true
  const suffix = `\n${RESTORED_METADATA}`
  if (!content.endsWith(suffix))
    return false
  const display = content.slice(0, -suffix.length)
  const endOfHeader = display.indexOf('\n')
  return endOfHeader > 0 && RESTORED_HEADERS.has(display.slice(0, endOfHeader)) && display.slice(endOfHeader + 1).trim() !== ''
}

/** Read only the text/image content parts that the current chat serializer emits. */
function messageText(content: unknown): string | undefined {
  if (typeof content === 'string')
    return content
  if (!Array.isArray(content))
    return undefined
  if (!content.every(part => isObject(part) && (
    (part.type === 'text' && typeof part.text === 'string')
    || (part.type === 'image_url' && isObject(part.image_url) && typeof part.image_url.url === 'string' && part.image_url.url !== '')
  ))) {
    return undefined
  }
  return nativeTextBlocks(content).join('\n')
}

/** Resolve request-local metadata compression without changing any original message. */
export function* codewhaleRequestMessages(messages: readonly unknown[]): Iterable<{ message: unknown, content: string | undefined }> {
  let lastFullMetadata: string | undefined
  for (const message of messages) {
    let content: string | undefined
    if (isObject(message) && message.role === 'user') {
      content = messageText(message.content)
      if (content === undefined) {
        lastFullMetadata = undefined
      }
      else if (content.endsWith(`\n${COMPRESSED_METADATA}`)) {
        if (lastFullMetadata !== undefined)
          content = content.slice(0, -COMPRESSED_METADATA.length) + lastFullMetadata
      }
      else if (content.endsWith('\n</turn_meta>')) {
        const start = content.lastIndexOf('\n<turn_meta>\n')
        if (start >= 0)
          lastFullMetadata = content.slice(start + 1)
        else if (content.startsWith('<turn_meta>\n'))
          lastFullMetadata = content
      }
    }
    else if (isObject(message) && (message.role === 'assistant' || message.role === 'system' || message.role === 'developer')) {
      // Non-user text can disturb the native compression cache without establishing runtime authority.
      if (nativeTextBlocks(message.content).some(text => text.trimStart().startsWith('<turn_meta>')))
        lastFullMetadata = undefined
    }
    else if (!isObject(message) || message.role !== 'tool') {
      lastFullMetadata = undefined
    }
    yield { message, content }
  }
}
