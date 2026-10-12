import { createHash, randomUUID } from 'node:crypto'
import { closeSync, constants, fsyncSync, linkSync, lstatSync, openSync, unlinkSync, writeFileSync } from 'node:fs'
import { isAbsolute, join, resolve } from 'node:path'

export const name = 'leapmux-image-receipts'
export const inject = ['tools']

const digest = data => createHash('sha256').update(data).digest('hex')

function directory(path) {
  if (typeof path !== 'string' || !isAbsolute(path) || resolve(path) !== path || path.includes('\0'))
    throw new Error('The image receipt directory requires a canonical absolute path.')
  const stat = lstatSync(path)
  if (!stat.isDirectory() || stat.isSymbolicLink())
    throw new Error('The image receipt directory must be a regular directory.')
  return path
}

function identity(exec) {
  const sessionId = exec.agent?.session.id
  if (typeof exec.token !== 'symbol' || typeof sessionId !== 'string' || sessionId === ''
    || typeof exec.callId !== 'string' || exec.callId === '' || typeof exec.name !== 'string' || exec.name === '')
    return undefined
  return { sessionId, callId: exec.callId, toolName: exec.name }
}

function keyTable(table, required) {
  if (typeof table !== 'object' || table === null)
    throw new Error('The image observer requires its generated field tables.')
  const keys = required.map(key => table[key])
  if (keys.some(key => typeof key !== 'string' || key === '') || new Set(keys).size !== keys.length)
    throw new Error('The image observer has an invalid generated field table.')
  return table
}

function imagePositions(result, config) {
  if (!Array.isArray(result.content) || typeof result.isError !== 'boolean')
    throw new Error('The native image result has no content list or error flag.')
  const positions = []
  const seen = new Map()
  const reference = config.imageReferenceFields
  const position = config.imagePositionFields
  for (let index = 0; index < result.content.length; index++) {
    const block = result.content[index]
    if (block?.type !== config.imageContentType)
      continue
    const ref = block.attachment
    const id = ref?.[reference.AttachmentID]
    if (typeof id !== 'string' || !/^sha256:[0-9a-f]{64}$/.test(id)
      || !['image/png', 'image/jpeg', 'image/webp', 'image/gif'].includes(ref[reference.MediaType])
      || ![reference.Bytes, reference.Width, reference.Height].every(key => Number.isSafeInteger(ref[key]) && ref[key] > 0))
      throw new Error('The native image result has an invalid attachment reference.')
    const original = seen.get(id)
    if (original !== undefined && Object.values(reference).some(key => original[key] !== ref[key]))
      throw new Error('The repeated native image references disagree about their metadata.')
    seen.set(id, ref)
    positions.push({ [position.Position]: index, [position.Attachment]: ref })
  }
  return positions
}

async function captureImages(ctx, positions, signal, config) {
  const attachments = ctx.get('attachments')
  if (attachments === undefined)
    throw new Error('The native image result has no attachment service.')
  const images = new Map()
  const reference = config.imageReferenceFields
  const position = config.imagePositionFields
  const value = config.imageValueFields
  for (const entry of positions) {
    const ref = entry[position.Attachment]
    const id = ref[reference.AttachmentID]
    if (images.has(id))
      continue
    const stored = await attachments.readImage(ref, signal)
    if (stored?.ref === undefined || Object.values(reference).some(key => stored.ref[key] !== ref[key])
      || !(stored.data instanceof Uint8Array) || stored.data.byteLength > config.maxImageBytes)
      throw new Error('The native image bytes do not match their attachment reference.')
    const data = Buffer.from(stored.data)
    if (data.byteLength !== ref[reference.Bytes] || `sha256:${digest(data)}` !== id)
      throw new Error('The native image bytes do not match their attachment digest.')
    images.set(id, { [value.Attachment]: stored.ref, [value.Data]: data.toString('base64') })
  }
  return [...images.values()]
}

function writeReceipt(root, receipt, fields, maximum) {
  const leaf = `${digest(receipt[fields.SessionID])}.${digest(receipt[fields.CallID])}.json`
  const destination = join(root, leaf)
  const pending = join(root, `.${leaf}.${randomUUID()}.pending`)
  const encoded = JSON.stringify(receipt)
  if (Buffer.byteLength(encoded, 'utf8') > maximum)
    throw new Error('The native image receipt exceeds its size limit.')
  let file
  let cause
  try {
    file = openSync(pending, constants.O_WRONLY | constants.O_CREAT | constants.O_EXCL | constants.O_NOFOLLOW, 0o600)
    writeFileSync(file, encoded)
    fsyncSync(file)
  } catch (error) {
    cause = error
  } finally {
    if (file !== undefined) {
      try { closeSync(file) } catch (error) { cause = cause ? new AggregateError([cause, error]) : error }
    }
  }
  if (cause !== undefined) {
    try { unlinkSync(pending) } catch (error) { cause = new AggregateError([cause, error]) }
    throw cause
  }
  // A native call identity commits once. A second commit cannot replace its image receipt.
  try {
    linkSync(pending, destination)
    unlinkSync(pending)
  } catch (error) {
    const failures = [error]
    try { unlinkSync(pending) } catch (cleanupError) { failures.push(cleanupError) }
    throw new AggregateError(failures)
  }
}

export function apply(ctx, config) {
  const root = directory(config.receiptDirectory)
  const fields = keyTable(config.imageReceiptFields, ['SessionID', 'CallID', 'ToolName', 'IsError', 'OriginalImages', 'RetainedImages', 'Images'])
  keyTable(config.imagePositionFields, ['Position', 'Attachment'])
  keyTable(config.imageReferenceFields, ['AttachmentID', 'MediaType', 'Bytes', 'Width', 'Height'])
  keyTable(config.imageValueFields, ['Attachment', 'Data'])
  if (!Number.isSafeInteger(config.maxImageBytes) || config.maxImageBytes <= 0 || typeof config.imageContentType !== 'string' || config.imageContentType === '')
    throw new Error('The native image receipt requires an image type and a positive byte limit.')
  const pending = new Map()
  ctx.on('tools/post-execute', async (exec, result, next) => {
    const owner = identity(exec)
    if (owner !== undefined && !pending.has(exec.token)) {
      try {
        const originalImages = imagePositions(result, config)
        if (originalImages.length !== 0) {
          const images = await captureImages(ctx, originalImages, exec.signal, config)
          pending.set(exec.token, { ...owner, originalImages, images })
        }
      } catch (error) {
        ctx.logger.warn('The native image receipt failed.', error)
      }
    }
    return next()
  }, { prepend: true })
  ctx.on('tools/result', (exec, result) => {
    const before = pending.get(exec.token)
    pending.delete(exec.token)
    const owner = identity(exec)
    if (before === undefined || owner === undefined || before.sessionId !== owner.sessionId
      || before.callId !== owner.callId || before.toolName !== owner.toolName)
      return
    const retainedImages = imagePositions(result, config)
    const reference = config.imageReferenceFields
    const position = config.imagePositionFields
    const originalRefs = new Map(before.originalImages.map(entry => {
      const ref = entry[position.Attachment]
      return [ref[reference.AttachmentID], ref]
    }))
    for (const entry of retainedImages) {
      const ref = entry[position.Attachment]
      const original = originalRefs.get(ref[reference.AttachmentID])
      if (original === undefined || Object.values(reference).some(key => original[key] !== ref[key]))
        throw new Error('The retained native image has another original attachment reference.')
    }
    writeReceipt(root, {
      [fields.SessionID]: owner.sessionId,
      [fields.CallID]: owner.callId,
      [fields.ToolName]: owner.toolName,
      [fields.IsError]: result.isError,
      [fields.OriginalImages]: before.originalImages,
      [fields.RetainedImages]: retainedImages,
      [fields.Images]: before.images,
    }, fields, config.maxImageBytes)
  })
  ctx.on('dispose', () => { pending.clear() })
}
