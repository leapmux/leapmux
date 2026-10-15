import type { MockModelScenarioStatus } from './mockModelScript'
import type { ModelScript } from './modelScriptFixture'
import { writeFile } from 'node:fs/promises'
import { withCleanup } from './cleanup'

/** The report methods that a model receipt uses. Unit callers can omit file attachments. */
export interface ModelReceiptReport {
  outputPath?: (name: string) => string
  attach?: (name: string, options: { path: string, contentType: string }) => Promise<void>
}

/** Store the complete receipt before attaching it. Serialization and write failures leave no successful attachment. */
export async function attachModelStatus(status: MockModelScenarioStatus, report: ModelReceiptReport): Promise<void> {
  if (!report.outputPath || !report.attach)
    return
  const path = report.outputPath('model-script.json')
  await writeFile(path, JSON.stringify(status), 'utf8')
  await report.attach('model-script', { path, contentType: 'application/json' })
}

/** Retain the actual requests after an operation, including failures, before the script cleanup removes them. */
export async function withModelReceipt<T>(
  script: Pick<ModelScript, 'status'>,
  report: ModelReceiptReport,
  operation: () => Promise<T>,
): Promise<T> {
  return withCleanup(operation, async () => attachModelStatus(await script.status(), report))
}
