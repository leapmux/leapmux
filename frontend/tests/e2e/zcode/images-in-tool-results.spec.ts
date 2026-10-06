import { zcodeTest } from '../zcode-fixtures'
import { exerciseZCodeNodeImage } from './nodeImageScenario'

zcodeTest('shows the picture emitted by the native Node tool', async ({ authenticatedZCodeWorkspace, native }) => {
  const workingDir = authenticatedZCodeWorkspace.workingDir
  if (!workingDir)
    throw new Error('ZCode test workspace has no working directory')
  await exerciseZCodeNodeImage(native, workingDir)
})
