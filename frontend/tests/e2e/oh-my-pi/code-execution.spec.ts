import { openNativeAgent } from '../helpers/nativeAgentOpen'
import { exerciseNativeCodeExecution, nativeCodeExecutionSchema } from '../helpers/nativeCodeExecution'
import { OH_MY_PI_AGENT, ohMyPiTest } from '../ohmypi-fixtures'
import { nativeContext } from './scenarios'

ohMyPiTest('runs native code and retains computed output and script errors after reload', async ({ authenticatedEmptyWorkspace, page, modelScript, leapmuxServer }) => {
  const context = await nativeContext({ page, modelScript, leapmuxServer, workspaceId: authenticatedEmptyWorkspace.workspaceId })
  await openNativeAgent(context, OH_MY_PI_AGENT, { directoryPrefix: 'native-code-execution-' })
  await exerciseNativeCodeExecution(context, {
    catalogProof: (request) => {
      nativeCodeExecutionSchema(request, 'eval', { language: 'string', code: 'string' })
    },
    scripts: marker => [
      { label: 'output', source: `console.log(${JSON.stringify(marker)} + (40 + 2));`, expected: `${marker}42`, failed: false },
      { label: 'failure', source: `throw new Error(${JSON.stringify(marker)} + (70 + 7));`, expected: `${marker}77`, failed: true },
    ],
  })
})
