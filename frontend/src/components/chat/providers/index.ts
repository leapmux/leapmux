// Import all provider modules to trigger side-effect registrations.
import './claude'
import './codex'
import './opencode'
import './stubs'

export { getProviderPlugin, registerProvider } from './registry'
export type { ProviderPlugin } from './registry'
