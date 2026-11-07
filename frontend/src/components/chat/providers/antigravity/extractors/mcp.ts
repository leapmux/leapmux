import type { McpRequest, McpResult } from '../../../model/tools/mcp'
import type { AntigravityStep } from './toolCommon'
import type { ImageResultSource } from '~/lib/imageBlocks'
import { parseImageBlock, withFallbackFilePath } from '~/lib/imageBlocks'
import { isObject, pickObject, pickString } from '~/lib/jsonPick'
import { fileUriToPath } from '~/lib/paths'

export function antigravityMcpRequest(step: AntigravityStep): McpRequest {
  const mcp = pickObject(step.raw, 'mcpTool')
  return {
    server: pickString(mcp, 'serverName'),
    tool: pickString(pickObject(mcp, 'toolCall'), 'name') || pickString(step.toolCall, 'name'),
    args: step.args,
  }
}

export function antigravityMcpResult(step: AntigravityStep): McpResult {
  return {
    content: [
      {
        type: 'text',
        text: pickString(pickObject(step.raw, 'mcpTool'), 'resultString'),
      },
      ...antigravityMcpImages(step).map(source => ({ type: 'image' as const, source })),
    ],
  }
}

function antigravityMcpImages(step: AntigravityStep): ImageResultSource[] {
  const mcp = pickObject(step.raw, 'mcpTool')
  const images = Array.isArray(mcp?.images)
    ? mcp.images
    : []
  return images.filter(isObject).flatMap((entry) => {
    const uri = pickString(entry, 'uri')
    const image = parseImageBlock({
      type: 'image',
      data: pickString(entry, 'base64Data'),
      mimeType: pickString(entry, 'mimeType'),
    })
    return image
      ? [
          withFallbackFilePath(image, fileUriToPath(uri) ?? uri),
        ]
      : []
  })
}
