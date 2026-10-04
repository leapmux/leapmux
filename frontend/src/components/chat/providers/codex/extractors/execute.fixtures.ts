// Exact raw execution notifications from installed Codex 0.159.3.
// An isolated scripted model API produced both native call pairs.

export const nativeExecSuccessRequest = {
  method: 'rawResponseItem/completed',
  params: {
    threadId: '01a0f856-4d74-76c3-a671-d352971ab263',
    turnId: '01a0f856-4d8b-74f0-8dac-380f5d3c40e1',
    item: {
      type: 'custom_tool_call',
      id: 'private-response-success-0-item',
      status: 'completed',
      call_id: 'native-success',
      name: 'exec',
      input: 'text("NATIVE_CODE_SUCCESS_" + (40 + 2));',
      internal_chat_message_metadata_passthrough: {
        turn_id: '01a0f856-4d8b-74f0-8dac-380f5d3c40e1',
      },
    },
  },
  emittedAtMs: 1790872800691,
}

export const nativeExecSuccessResult = {
  method: 'rawResponseItem/completed',
  params: {
    threadId: '01a0f856-4d74-76c3-a671-d352971ab263',
    turnId: '01a0f856-4d8b-74f0-8dac-380f5d3c40e1',
    item: {
      type: 'custom_tool_call_output',
      id: 'ctco_01a0f856-4dd1-7df3-baad-37767de04096',
      call_id: 'native-success',
      output: [
        {
          type: 'input_text',
          text: 'Script completed\nWall time 0.0 seconds\nOutput:\n',
        },
        {
          type: 'input_text',
          text: 'NATIVE_CODE_SUCCESS_42',
        },
      ],
      internal_chat_message_metadata_passthrough: {
        turn_id: '01a0f856-4d8b-74f0-8dac-380f5d3c40e1',
        create_time: 1790872800.721981,
      },
    },
  },
  emittedAtMs: 1790872800722,
}

export const nativeExecFailureRequest = {
  method: 'rawResponseItem/completed',
  params: {
    threadId: '01a0f856-4d74-76c3-a671-d352971ab263',
    turnId: '01a0f856-4dd7-79f0-ab59-23ea9136a2bb',
    item: {
      type: 'custom_tool_call',
      id: 'private-response-failure-0-item',
      status: 'completed',
      call_id: 'native-failure',
      name: 'exec',
      input: 'throw new Error("NATIVE_CODE_FAILURE");',
      internal_chat_message_metadata_passthrough: {
        turn_id: '01a0f856-4dd7-79f0-ab59-23ea9136a2bb',
      },
    },
  },
  emittedAtMs: 1790872800738,
}

export const nativeExecFailureResult = {
  method: 'rawResponseItem/completed',
  params: {
    threadId: '01a0f856-4d74-76c3-a671-d352971ab263',
    turnId: '01a0f856-4dd7-79f0-ab59-23ea9136a2bb',
    item: {
      type: 'custom_tool_call_output',
      id: 'ctco_01a0f856-4de4-70d3-aa32-6a028d808de7',
      call_id: 'native-failure',
      output: [
        {
          type: 'input_text',
          text: 'Script failed\nWall time 0.0 seconds\nOutput:\n',
        },
        {
          type: 'input_text',
          text: 'Script error:\nError: NATIVE_CODE_FAILURE\n    at exec_main.mjs:1:7',
        },
      ],
      internal_chat_message_metadata_passthrough: {
        turn_id: '01a0f856-4dd7-79f0-ab59-23ea9136a2bb',
        create_time: 1790872800.740003,
      },
    },
  },
  emittedAtMs: 1790872800740,
}
