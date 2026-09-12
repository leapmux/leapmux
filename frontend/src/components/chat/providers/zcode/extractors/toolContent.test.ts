import { describe, expect, it } from 'vitest'
import { input } from '../../testUtils'
import { zcodeToolResultImages } from './image'
import { zcodeNativeTool, zcodeRow } from './toolCommon'
import { zcodeMcpContent } from './toolContent'

function fixture(child = false) {
  const sessionId = child ? 'child-session' : 'session'
  const toolCallId = child ? 'tool_subagent_agent_call' : 'call'
  const uri = `zcode-artifact://${sessionId}/tool-result-image`
  const original = {
    type: 'tool.updated',
    payload: {
      kind: 'result',
      toolCallId,
      ...(child ? { agentId: 'agent', childSessionId: sessionId } : {}),
      result: { success: true, content: 'Original attachment text', display: { kind: 'mcp_tool', serverName: 'docs', toolName: 'read' } },
    },
  }
  const supplemental = {
    type: original.type,
    payload: { kind: 'result', toolCallId },
    nativeTool: {
      id: 'part',
      sessionId,
      messageId: 'message',
      data: {
        type: 'tool',
        callID: 'call',
        tool: 'mcp__docs__read',
        state: {
          status: 'completed',
          input: {},
          metadata: { modelContentLayout: [
            { type: 'text', text: 'Before' },
            { type: 'attachment', attachmentIndex: 0 },
            { type: 'text', text: 'After' },
          ] },
          attachments: [{ type: 'file', sessionID: sessionId, messageID: 'message', mime: 'image/png', filename: 'MCP image', url: uri }],
        },
      },
    },
    artifacts: { [uri]: 'data:image/png;base64,AQID' },
  }
  const row = zcodeRow(original, 'mcp__docs__read', undefined, supplemental)
  return { original, supplemental, row, uri }
}

describe('stored ZCode tool content', () => {
  it('keeps image positions and omits the generated MCP image caption', () => {
    const { row, original } = fixture()
    const before = JSON.stringify(original)
    const content = zcodeMcpContent(row)
    expect(content?.map(item => item.type)).toEqual(['text', 'image', 'text'])
    expect(zcodeToolResultImages(row)).toEqual([{ mimeType: 'image/png', url: 'data:image/png;base64,AQID', description: undefined }])
    expect(JSON.stringify(original)).toBe(before)
  })

  it('keeps a missing image slot for the image viewer', () => {
    const { row, supplemental, uri } = fixture()
    delete supplemental.artifacts[uri]
    const images = zcodeToolResultImages(row)
    expect(images).toHaveLength(1)
    expect(images[0].url).toBeUndefined()
    expect(zcodeMcpContent(row)?.map(item => item.type)).toEqual(['text', 'image', 'text'])
  })

  it('accepts image MIME types without case sensitivity', () => {
    const { row, supplemental } = fixture()
    supplemental.nativeTool.data.state.attachments[0].mime = 'IMAGE/PNG'
    expect(zcodeToolResultImages(row)).toHaveLength(1)
  })

  it('resolves the projected child tool ID without changing the native record', () => {
    const { row, supplemental } = fixture(true)
    expect(zcodeToolResultImages(row)).toHaveLength(1)
    expect(supplemental.nativeTool.data.callID).toBe('call')
  })

  it('recovers child identity from the matching request', () => {
    const { row, original, supplemental } = fixture(true)
    const request = { type: original.type, payload: { ...original.payload, kind: 'scheduled', toolName: 'mcp__docs__read' } }
    row.parsed = { ...original, payload: { ...original.payload, agentId: undefined, childSessionId: undefined } }
    row.toolUseParsed = input(request)
    expect(zcodeToolResultImages(row)).toHaveLength(1)
    supplemental.nativeTool.sessionId = 'foreign-session'
    expect(zcodeToolResultImages(row)).toHaveLength(0)
  })

  it.each(['call', 'tool', 'session', 'message', 'status'])('rejects a conflicting %s identity', (field) => {
    const { row, supplemental } = fixture()
    const native = supplemental.nativeTool
    if (field === 'call')
      native.data.callID = 'foreign-call'
    if (field === 'tool')
      native.data.tool = 'foreign-tool'
    if (field === 'session')
      native.sessionId = 'foreign-session'
    if (field === 'message')
      native.messageId = 'foreign-message'
    if (field === 'status')
      native.data.state.status = 'running'
    expect(zcodeMcpContent(row)).toBeNull()
  })

  it('keeps the original content when the stored layout is empty or invalid', () => {
    const { row, supplemental } = fixture()
    const layout = supplemental.nativeTool.data.state.metadata.modelContentLayout
    layout.length = 0
    expect(zcodeMcpContent(row)).toBeNull()
    layout.push({ type: 'attachment', attachmentIndex: -1 })
    expect(zcodeMcpContent(row)).toBeNull()
    layout[0].attachmentIndex = 10
    expect(zcodeMcpContent(row)).toBeNull()
  })

  it('rejects a child tool ID without a native suffix', () => {
    const { row, original, supplemental } = fixture(true)
    row.parsed = { ...original, payload: { ...original.payload, toolCallId: 'tool_subagent_agent_' } }
    supplemental.payload.toolCallId = 'tool_subagent_agent_'
    supplemental.nativeTool.data.callID = ''
    expect(zcodeNativeTool(row)).toBeNull()
  })
})
