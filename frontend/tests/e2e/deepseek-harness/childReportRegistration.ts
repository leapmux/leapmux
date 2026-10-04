import type { ModelScript } from '../helpers/modelScriptFixture'
import { deepseekHarnessChildReportRule } from './childReports'

const registrations = new WeakMap<Pick<ModelScript, 'rule'>, Map<string, Promise<string>>>()

export async function registerDeepseekHarnessChildReport(script: Pick<ModelScript, 'rule'>, childSessionId: string): Promise<string> {
  const rule = deepseekHarnessChildReportRule(childSessionId)
  const registered = registrations.get(script) ?? new Map<string, Promise<string>>()
  if (!registrations.has(script))
    registrations.set(script, registered)
  const existing = registered.get(rule.name)
  if (existing)
    return existing
  const pending = Promise.resolve().then(() => script.rule(rule)).then(() => rule.name).catch((cause) => {
    if (registered.get(rule.name) === pending)
      registered.delete(rule.name)
    throw cause
  })
  registered.set(rule.name, pending)
  return pending
}
