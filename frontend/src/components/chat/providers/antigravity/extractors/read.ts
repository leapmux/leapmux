import type { NumberedFileLine, ReadFileResult } from '../../../model/readFileResult'
import type { ReadRequest } from '../../../model/tools/read'
import type { AntigravityStep } from './toolCommon'
import type { ImageResultSource } from '~/lib/imageBlocks'
import { parseImageBlock, withFallbackFilePath } from '~/lib/imageBlocks'
import { isObject, pickObject, pickString } from '~/lib/jsonPick'

export function antigravityReadRequest(step: AntigravityStep): ReadRequest {
  const view = pickObject(step.raw, 'viewFile')
  const binaryURI = pickString(pickObject(view, 'binaryData'), 'uri') || pickString(pickObject(view, 'mediaData'), 'uri')
  return { path: pickString(step.args, 'Path') || filePathFromURI(pickString(view, 'absolutePathUri') || binaryURI) }
}

export function antigravityReadResult(step: AntigravityStep): ReadFileResult {
  const content = pickString(pickObject(step.raw, 'viewFile'), 'content')
  const body = content.replace(/^Showing lines \d+-\d+ of \d+\n?/, '')
  if (!body)
    return { lines: [], fallbackContent: '' }
  const lines: NumberedFileLine[] = []
  for (const line of body.split('\n')) {
    const match = /^(\d+): ?(.*)$/.exec(line)
    if (!match?.[1])
      return { lines: null, fallbackContent: body }
    lines.push({
      num: Number(match[1]),
      text: match[2] ?? '',
    })
  }
  return { lines, fallbackContent: body }
}

export function filePathFromURI(uri: string): string {
  if (!uri.startsWith('file:'))
    return uri
  try {
    const path = decodeURIComponent(new URL(uri).pathname)
    return /^\/[A-Z]:/i.test(path)
      ? path.slice(1)
      : path
  }
  catch {
    return uri
  }
}

export function antigravityReadImages(step: AntigravityStep): ImageResultSource[] {
  const view = pickObject(step.raw, 'viewFile')
  const binary = pickObject(view, 'binaryData')
  const media = pickObject(view, 'mediaData')
  const data = pickString(binary, 'base64Data') || pickString(media, 'inlineData')
  if (!data)
    return []
  const image = parseImageBlock({
    type: 'image',
    data,
    mimeType: pickString(binary, 'mimeType') || pickString(media, 'mimeType'),
  })
  return image
    ? [
        withFallbackFilePath(image, filePathFromURI(pickString(binary, 'uri') || pickString(media, 'uri') || pickString(view, 'absolutePathUri'))),
      ]
    : []
}

export function antigravityMcpImages(step: AntigravityStep): ImageResultSource[] {
  const mcp = pickObject(step.raw, 'mcpTool')
  const images = Array.isArray(mcp?.images)
    ? mcp.images
    : []
  return images.filter(isObject).flatMap((entry) => {
    const image = parseImageBlock({
      type: 'image',
      data: pickString(entry, 'base64Data'),
      mimeType: pickString(entry, 'mimeType'),
    })
    return image
      ? [
          withFallbackFilePath(image, filePathFromURI(pickString(entry, 'uri'))),
        ]
      : []
  })
}
