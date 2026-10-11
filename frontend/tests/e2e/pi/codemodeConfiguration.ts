import { mkdirSync, writeFileSync } from 'node:fs'
import { join } from 'node:path'
import { assertPrivateNativeAncestor } from '../helpers/nativeConfigurationFile'

/** Activate Pi's existing native executor in the isolated project extension. */
export function activateNativeCodemode(directory: string, runDirectory: string): string {
  assertPrivateNativeAncestor(directory, runDirectory, { refuseSymlink: true })
  const project = join(directory, '.pi')
  assertPrivateNativeAncestor(project, runDirectory, { refuseSymlink: true })
  mkdirSync(project, { recursive: true })
  const extensions = join(project, 'extensions')
  assertPrivateNativeAncestor(extensions, runDirectory, { refuseSymlink: true })
  mkdirSync(extensions, { recursive: true })
  const path = join(extensions, 'native-codemode.ts')
  assertPrivateNativeAncestor(path, runDirectory, { refuseSymlink: true })
  writeFileSync(path, 'export default function (pi) { pi.on(\'session_start\', () => { pi.setActiveTools([...pi.getActiveTools(), \'codemode\']); }); }')
  return path
}
