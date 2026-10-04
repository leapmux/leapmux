import { describe, expect, it } from 'vitest'
import { lettaNativeFinalReturn } from './nativeFinalReturn'

const final = { message_type: 'tool_return_message', tool_call_id: 'native-call', status: 'success', tool_return: 'final native overflow reference' }

describe('lettaNativeFinalReturn', () => {
  it('selects the exact final model result among successful streamed returns with the same call ID', () => {
    const earlier = { ...final, tool_return: 'earlier native output window' }
    expect(lettaNativeFinalReturn([earlier, final], 'native-call', final.tool_return)).toBe(final)
  })

  it('does not require the Worker span prefix to equal the native call ID', () => {
    const frame = { ...final, spanId: 'letta-tool-native-call' }
    expect(lettaNativeFinalReturn([frame], 'native-call', final.tool_return)).toBe(frame)
  })

  it('rejects repeated or foreign final returns', () => {
    expect(() => lettaNativeFinalReturn([final, final], 'native-call', final.tool_return)).toThrow('unique')
    expect(() => lettaNativeFinalReturn([final], 'other', final.tool_return)).toThrow('unique')
    expect(() => lettaNativeFinalReturn([final], 'native-call', 'other text')).toThrow('unique')
    expect(() => lettaNativeFinalReturn([{ ...final, status: 'error' }], 'native-call', final.tool_return)).toThrow('unique')
    expect(() => lettaNativeFinalReturn([], '', final.tool_return)).toThrow('exact')
  })

  it('rejects a progress snapshot as the final native result', () => {
    const progress = { ...final, id: 'synthetic-tool-return-stream-native-call' }
    expect(() => lettaNativeFinalReturn([progress], 'native-call', final.tool_return)).toThrow('unique')
    expect(lettaNativeFinalReturn([progress, { ...final, id: 'synthetic-tool-return-actual-final' }], 'native-call', final.tool_return)).toMatchObject({ id: 'synthetic-tool-return-actual-final' })
  })

  it('selects the native final result after native progress and an empty lifecycle end', () => {
    const first = { ...final, id: 'synthetic-tool-return-stream-native-call', tool_return: 'first current window' }
    const second = { ...first, tool_return: 'second current window' }
    const end = { message_type: 'client_tool_end', tool_call_id: 'native-call', status: 'success' }
    const actual = { ...final, id: 'synthetic-tool-return-actual-final' }
    expect(lettaNativeFinalReturn([first, second, end, actual], 'native-call', final.tool_return)).toBe(actual)
  })
})
