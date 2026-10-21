import { describe, expect, it } from 'vitest'
import { AgentProvider } from '~/generated/proto/leapmux/v1/agent_pb'
import { input } from '../testUtils'
import { classifyCommandCodeMessage } from './classification'

describe('classifyCommandCodeMessage', () => {
  it.each(['compaction_start', 'compaction_done', 'run_start', 'run_end', 'model_request_end', 'tool_update', 'subagent_start', 'subagent_stop', 'permission_mode_changed', 'config_setting_changed'])('draws a stored %s as unknown', (type) => {
    expect(classifyCommandCodeMessage(input({ type: 'event', event: { type } }, null, AgentProvider.COMMAND_CODE))).toEqual({ kind: 'unknown' })
  })
})
