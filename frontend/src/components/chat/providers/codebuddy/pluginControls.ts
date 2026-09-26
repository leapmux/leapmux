import type { ProviderControlCapability } from '../capabilities'
import { codebuddyExtractControl } from './extractControl'

/**
 * The CodeBuddy control channel.
 *
 * The answer the worker sends is CodeBuddy's own `{"allowed":true}`, so the
 * shared Allow/Deny envelope is what the browser sends and the worker
 * translates. The options stay empty for the same reason: the shared pair is
 * the surface the reader answers.
 */
export const codebuddyControls: ProviderControlCapability = {
  extractControl: codebuddyExtractControl,
}
