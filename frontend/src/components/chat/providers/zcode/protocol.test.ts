import { describe, expect, it } from 'vitest'
import { ZCODE_DISPLAY } from './protocol'

// These display values belong to the frontend. Shared values come from the contract.

describe('zcode display kinds (ZCODE_DISPLAY)', () => {
  it('matches the installed provider display variants', () => {
    expect(ZCODE_DISPLAY).toEqual({
      FileDiff: 'file_diff',
      McpTool: 'mcp_tool',
      ComputerUse: 'cua',
      TaskOutput: 'task_output',
      TaskStop: 'task_stop',
      LocalAgentMessage: 'local_agent_message',
      RespondToCoordinator: 'respond_to_coordinator',
      NodeImages: 'node_repl_images',
    })
  })
})
