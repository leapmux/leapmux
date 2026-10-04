import { describe, expect, it } from 'vitest'
import { piNativeMcpContent } from './mcp'
import { nativeMcpResourceImage } from './mcp.fixtures'

describe('piNativeMcpContent', () => {
  it('reads the captured resource image from the complete native resource result', () => {
    expect(piNativeMcpContent(nativeMcpResourceImage.toolName, nativeMcpResourceImage.result)).toEqual(nativeMcpResourceImage.result.structuredContent.contents.map(resource => ({ type: 'resource', resource })))
  })

  it('reads full native blocks without the model text copy', () => {
    const content = [{ type: 'text', text: 'Complete output' }, { type: 'image', data: 'image', mimeType: 'image/png' }]
    expect(piNativeMcpContent('mcp__sample__lookup', { content: [{ type: 'text', text: 'Truncated model text' }], details: { server: 'sample', tool: 'lookup' }, structuredContent: { content } })).toEqual(content)
  })

  it('wraps resource contents once for the shared resource and image readers', () => {
    const contents = [{ uri: 'probe://image', blob: 'image', mimeType: 'image/png' }, { uri: 'probe://text', text: 'Resource body', mimeType: 'text/plain' }]
    expect(piNativeMcpContent('read_mcp_resource', { details: { server: 'sample', tool: 'read_mcp_resource' }, structuredContent: { contents } })).toEqual(contents.map(resource => ({ type: 'resource', resource })))
  })

  it.each([{ content: [] }, { content: [{ type: 'text', text: '' }] }])('preserves complete empty content: $content', ({ content }) => {
    expect(piNativeMcpContent('mcp__sample__lookup', { details: { server: 'sample', tool: 'lookup' }, structuredContent: { content } })).toEqual(content)
  })

  it('preserves empty resource contents', () => {
    expect(piNativeMcpContent('read_mcp_resource', { details: { server: 'sample', tool: 'read_mcp_resource' }, structuredContent: { contents: [] } })).toEqual([])
  })

  it.each([{ structuredContent: undefined }, { structuredContent: null }, { structuredContent: [] }, { structuredContent: 'invalid' }, { structuredContent: { content: null } }, { structuredContent: { content: {} } }])('rejects malformed native blocks: $structuredContent', ({ structuredContent }) => {
    expect(piNativeMcpContent('mcp__sample__lookup', { details: { server: 'sample', tool: 'lookup' }, structuredContent })).toBeNull()
  })

  it('does not use structured content from an unrelated extension', () => {
    expect(piNativeMcpContent('custom', { details: { count: 0 }, structuredContent: { content: [{ type: 'text', text: 'Extension state' }] } })).toBeNull()
  })

  it('preserves malformed blocks for the shared unknown-content reader', () => {
    const content = [null, 0, { type: 'unknown', value: false }]
    expect(piNativeMcpContent('mcp__sample__lookup', { details: { server: 'sample', tool: 'lookup' }, structuredContent: { content } })).toEqual(content)
  })

  it('does not treat tool arguments as resource contents', () => {
    expect(piNativeMcpContent('mcp__sample__lookup', { details: { server: 'sample', tool: 'lookup' }, structuredContent: { contents: [{ uri: 'probe://wrong', text: 'Wrong shape' }] } })).toBeNull()
  })
})
