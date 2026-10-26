import { Buffer } from 'node:buffer'
import { writeFileSync } from 'node:fs'
import { join } from 'node:path'
import officeDocuments from '../../../../testdata/office-documents.json' with { type: 'json' }

export type OfficeDocumentKind = keyof typeof officeDocuments.documents

/** Write a valid Office document, retaining its filename when its content changes. */
export function writeOfficeAttachmentFixture(directory: string, kind: OfficeDocumentKind, updated = false): string {
  const document = officeDocuments.documents[kind]
  const path = join(directory, document.filename)
  writeFileSync(path, Buffer.from(updated ? document.updatedBase64 : document.originalBase64, 'base64'))
  return path
}
