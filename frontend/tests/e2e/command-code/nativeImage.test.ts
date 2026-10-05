import type { NativeMessageSnapshot } from '../helpers/nativeMessages'
import { create } from '@bufbuild/protobuf'
import { describe, expect, it } from 'vitest'
import { AgentChatMessageSchema, ContentCompression } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { readCommandCodeNativeImage } from './nativeImage'

const image = { type: 'image', source: { type: 'base64', media_type: 'image/jpeg', data: '/9j/4AAQSkZJRg==' } }

function completed(result: unknown[], overrides: Record<string, unknown> = {}) {
  return { type: 'event', event: { type: 'tool_completed', toolCallId: 'native-call', toolName: 'read_file', result, ...overrides } }
}

function snapshot(...frames: unknown[]): NativeMessageSnapshot {
  return { agentId: 'agent', agentSessionId: 'native-session', messages: frames.map((value, index) => create(AgentChatMessageSchema, {
    id: `native-row-${index}`,
    agentSessionId: 'native-session',
    spanId: 'native-call',
    contentCompression: ContentCompression.NONE,
    content: new TextEncoder().encode(JSON.stringify(value)),
  })) }
}

// Source: Command Code 1.74.1 compresses a file image before it attaches the image (dist/cli.mjs, the
// read_file image branch states the compressed media type), so the bytes of the native result differ
// from the bytes of the file. The native result states the image that the model receives.
describe('readCommandCodeNativeImage', () => {
  it('reads the media type and the bytes that the native result states', () => {
    const result = readCommandCodeNativeImage(snapshot(completed([{ type: 'text', text: 'Read image native.png and attached it below for viewing (643 B, image/jpeg).' }, image])), 'native-call')
    expect(result).toEqual({ mediaType: 'image/jpeg', data: '/9j/4AAQSkZJRg==' })
  })

  // Each source case keeps the two fields that its check does not read valid, so only its own check can refuse it.
  it.each([
    ['no image block', [{ type: 'text', text: 'Read a text file.' }], 'exactly one image block'],
    ['two image blocks', [image, image], 'exactly one image block'],
    ['an image block that is not base64', [{ type: 'image', source: { type: 'url', media_type: 'image/png', data: 'aGk=' } }], 'base64 image source'],
    ['an image block without bytes', [{ type: 'image', source: { type: 'base64', media_type: 'image/png', data: '' } }], 'base64 image source'],
    ['a media type that is not an image', [{ type: 'image', source: { type: 'base64', media_type: 'text/plain', data: 'aGk=' } }], 'base64 image source'],
  ])('refuses a result with %s', (_name, result, message) => {
    expect(() => readCommandCodeNativeImage(snapshot(completed(result)), 'native-call')).toThrow(message)
  })

  it('refuses the result of another tool or another call', () => {
    expect(() => readCommandCodeNativeImage(snapshot(completed([image], { toolName: 'shell_command' })), 'native-call')).toThrow('exactly one')
    expect(() => readCommandCodeNativeImage(snapshot(completed([image], { toolCallId: 'foreign-call' })), 'native-call')).toThrow('exactly one')
  })

  it('refuses a call that has two completed records', () => {
    expect(() => readCommandCodeNativeImage(snapshot(completed([image]), completed([image])), 'native-call')).toThrow('exactly one')
  })
})
