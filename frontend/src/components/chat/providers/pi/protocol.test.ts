import { describe, expect, it } from 'vitest'
import { PI_ASSISTANT_EVENT, PI_DIALOG_METHOD, PI_EVENT, PI_EXTENSION_METHOD, PI_TOOL } from './protocol'

describe('pi event constants (PI_EVENT)', () => {
  it('pins each event-type literal to its wire format', () => {
    expect(PI_EVENT).toEqual({
      AgentStart: 'agent_start',
      AgentEnd: 'agent_end',
      TurnStart: 'turn_start',
      TurnEnd: 'turn_end',
      MessageStart: 'message_start',
      MessageUpdate: 'message_update',
      MessageEnd: 'message_end',
      ToolExecutionStart: 'tool_execution_start',
      ToolExecutionEnd: 'tool_execution_end',
      ToolExecutionUpdate: 'tool_execution_update',
      ExtensionUIRequest: 'extension_ui_request',
      ExtensionUIResponse: 'extension_ui_response',
      ExtensionError: 'extension_error',
      CompactionStart: 'compaction_start',
      CompactionEnd: 'compaction_end',
      AutoRetryStart: 'auto_retry_start',
      AutoRetryEnd: 'auto_retry_end',
      QueueUpdate: 'queue_update',
      Response: 'response',
    })
  })
})

describe('pi assistant event constants (PI_ASSISTANT_EVENT)', () => {
  it('pins streaming delta sub-types', () => {
    expect(PI_ASSISTANT_EVENT).toEqual({
      TextDelta: 'text_delta',
      ThinkingDelta: 'thinking_delta',
    })
  })
})

describe('pi dialog method constants (PI_DIALOG_METHOD)', () => {
  it('pins the four dialog methods used for control requests', () => {
    expect(PI_DIALOG_METHOD).toEqual({
      Select: 'select',
      Confirm: 'confirm',
      Input: 'input',
      Editor: 'editor',
    })
  })
})

describe('pi extension method constants (PI_EXTENSION_METHOD)', () => {
  it('pins the fire-and-forget extension UI methods', () => {
    expect(PI_EXTENSION_METHOD).toEqual({
      Notify: 'notify',
      SetStatus: 'setStatus',
      SetWidget: 'setWidget',
      SetTitle: 'setTitle',
      SetEditorText: 'set_editor_text',
    })
  })
})

describe('pi tool constants (PI_TOOL)', () => {
  it('pins the canonical tool identifiers Pi uses on tool_execution_*', () => {
    expect(PI_TOOL).toEqual({
      Bash: 'bash',
      Read: 'read',
      Edit: 'edit',
      Write: 'write',
    })
  })
})
