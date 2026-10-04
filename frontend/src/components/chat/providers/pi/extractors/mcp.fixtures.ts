// Captured from installed Pi 0.99.1 through an isolated scripted model API.
// The original capture is .tmp/pi099-native-mcp-probe/native-frames.json.

export const nativeMcpEcho = {
  type: 'tool_execution_end',
  toolCallId: 'native-echo',
  toolName: 'mcp__probe__echo',
  result: {
    content: [
      {
        type: 'text',
        text: 'PI099_ECHO:native-zero',
      },
    ],
    details: {
      server: 'probe',
      tool: 'echo',
    },
    structuredContent: {
      content: [
        {
          type: 'text',
          text: 'PI099_ECHO:native-zero',
        },
      ],
      structuredContent: {
        nativeName: 'echo',
        zero: 0,
        empty: '',
      },
    },
  },
  isError: false,
}

export const nativeMcpResourceImage = {
  type: 'tool_execution_end',
  toolCallId: 'native-resource_image',
  toolName: 'read_mcp_resource',
  result: {
    content: [
      {
        type: 'image',
        data: 'iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mP8/x8AAwMCAO+jG1sAAAAASUVORK5CYII=',
        mimeType: 'image/png',
      },
    ],
    details: {
      server: 'probe',
      tool: 'read_mcp_resource',
    },
    structuredContent: {
      server: 'probe',
      uri: 'probe://image',
      contents: [
        {
          uri: 'probe://image',
          blob: 'iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mP8/x8AAwMCAO+jG1sAAAAASUVORK5CYII=',
          mimeType: 'image/png',
        },
      ],
    },
  },
  isError: false,
}

export const nativeCodemodeFailure = {
  type: 'tool_execution_end',
  toolCallId: 'native-codemode_failure',
  toolName: 'codemode',
  result: {
    content: [
      {
        type: 'text',
        text: 'Script failed\nWall time 0.0 seconds\nOutput:\n',
      },
      {
        type: 'text',
        text: 'Script error:\nError: PI099_CODEMODE_FAILURE\n    at <anonymous> (codemode.js:1:39)\n\nNo tool calls were made.',
      },
    ],
    details: {
      calls: [],
    },
    isError: true,
  },
  isError: true,
}
