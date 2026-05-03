import { render } from '@solidjs/testing-library'
import Eye from 'lucide-solid/icons/eye'
import { describe, expect, it } from 'vitest'
import { ACP_TOOL_KIND, CLAUDE_TOOL } from '~/types/toolMessages'
import { kindIcon } from './acp/renderers/helpers'
import { toolIconFor } from './claude/toolUse/icons'
import { PiToolExecutionRenderer } from './pi/renderers/toolExecution'

describe('read tool icons', () => {
  it('uses the Eye icon for Pi read tool uses', () => {
    const { container } = render(() => PiToolExecutionRenderer({
      parsed: {
        type: 'tool_execution_start',
        toolName: 'read',
        args: { path: '/tmp/a.ts' },
      },
    }))

    expect(container.querySelector('svg.lucide-eye')).not.toBeNull()
  })

  it('uses the Eye icon for Claude Code Read tool uses', () => {
    expect(toolIconFor(CLAUDE_TOOL.READ)).toBe(Eye)
  })

  it('uses the Eye icon for ACP read tool uses', () => {
    expect(kindIcon(ACP_TOOL_KIND.READ)).toBe(Eye)
  })
})
