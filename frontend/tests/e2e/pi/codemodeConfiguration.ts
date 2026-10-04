import { mkdirSync, writeFileSync } from 'node:fs'
import { join } from 'node:path'
import { assertPiConfigurationPath } from './privateConfigurationPath'

/** Activate Pi's existing native executor in the isolated project extension. */
export function activateNativeCodemode(directory: string, runDirectory: string): string {
  assertPiConfigurationPath(directory, runDirectory)
  const project = join(directory, '.pi')
  assertPiConfigurationPath(project, runDirectory)
  mkdirSync(project, { recursive: true })
  const extensions = join(project, 'extensions')
  assertPiConfigurationPath(extensions, runDirectory)
  mkdirSync(extensions, { recursive: true })
  const path = join(extensions, 'native-codemode.ts')
  assertPiConfigurationPath(path, runDirectory)
  writeFileSync(path, 'export default function (pi) { pi.on(\'session_start\', () => { pi.setActiveTools([...pi.getActiveTools(), \'codemode\']); }); }')
  return path
}
