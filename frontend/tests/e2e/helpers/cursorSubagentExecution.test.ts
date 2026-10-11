import { describe, expect, it } from 'vitest'
import { encodeLengthDelimited, encodeStringField } from './cursorProtobuf'
import { CursorSubagentExecution } from './cursorSubagentExecution'
import { cursorSubagentReplyFixture, cursorSubagentSuccessFixture } from './cursorTestFrames'

const call = { callID: 'native-task', description: 'Actual native task', prompt: 'Execute the actual child.', modelID: 'auto', parentConversationID: 'actual-parent' }
const success = () => cursorSubagentSuccessFixture({ agentID: 'actual-child', finalMessage: 'ACTUAL_CHILD_REPORT' })

describe('CursorSubagentExecution', () => {
  it('requests one actual native child and completes from its exact matching reply', () => {
    const execution = new CursorSubagentExecution(call, 4)
    expect(execution.started).toBeInstanceOf(Uint8Array)
    expect(execution.request()).toBeInstanceOf(Uint8Array)
    const result = execution.acceptReply(cursorSubagentReplyFixture(4, success()))
    expect(result?.reply).toMatchObject({ success: true, agentID: 'actual-child', finalMessage: 'ACTUAL_CHILD_REPORT' })
    expect(result?.update).toBeInstanceOf(Uint8Array)
    expect(() => execution.request()).toThrow('already completed')
    expect(() => execution.acceptReply(cursorSubagentReplyFixture(4, success()))).toThrow('another completed reply')
  })

  it('rejects wrong native execution and tool IDs without completing the state', () => {
    const execution = new CursorSubagentExecution(call, 4)
    expect(() => execution.acceptReply(cursorSubagentReplyFixture(5, success()))).toThrow('different execution or tool ID')
    expect(() => execution.acceptReply(cursorSubagentReplyFixture(4, success(), 'other-tool'))).toThrow('different execution or tool ID')
    expect(execution.acceptReply(cursorSubagentReplyFixture(4, success()))?.reply).toMatchObject({ agentID: 'actual-child' })
  })

  it('leaves the execution pending after an unrelated native frame', () => {
    const execution = new CursorSubagentExecution(call, 4)
    expect(execution.acceptReply(encodeLengthDelimited(1, encodeStringField(1, 'heartbeat')))).toBeUndefined()
    expect(execution.request()).toBeInstanceOf(Uint8Array)
  })
})
