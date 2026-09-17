import Eye from 'lucide-solid/icons/eye'
import { describe, expect, it } from 'vitest'
import { ACP_TOOL_KIND } from '~/generated/contracts/acp-protocol'
import { rendererFor } from '../results/tools'
import { claudeToolIcon, claudeToolKind } from './claude/toolKinds'
import { CLAUDE_TOOL_NAMES } from './claude/toolNames'
import { piToolCallIR, piToolRow } from './pi/extractors/toolCall'

describe('read tool icons', () => {
  // Pi states no icon of its own, so the row takes the KIND's icon -- which is the
  // point: a read on Pi and a read on Claude must not draw two different glyphs.
  it('uses the Eye icon for Pi read tool uses', () => {
    const row = piToolRow({
      type: 'tool_execution_start',
      toolCallId: 'call-read',
      toolName: 'read',
      args: { path: '/tmp/a.ts' },
    }, undefined, undefined)!
    const call = piToolCallIR(row)
    expect(call.icon).toBeUndefined()
    expect(rendererFor(call).icon).toBe(Eye)
  })

  // Claude states no icon of its own for a read either: the tool maps to the
  // shared `read` kind, and the kind owns the glyph.
  it('uses the Eye icon for Claude Code Read tool uses', () => {
    expect(claudeToolIcon(CLAUDE_TOOL_NAMES.READ)).toBeUndefined()
    expect(rendererFor({ kind: claudeToolKind(CLAUDE_TOOL_NAMES.READ) }).icon).toBe(Eye)
  })

  it('uses the Eye icon for ACP read tool uses', () => {
    expect(rendererFor({ kind: ACP_TOOL_KIND.Read }).icon).toBe(Eye)
  })
})
