import { createClient } from '@connectrpc/connect'
import { AdminSettingsService, AdminUserService } from '~/generated/proto/leapmux/v1/admin_pb'
import { AppService } from '~/generated/proto/leapmux/v1/app_pb'
import { AuthService } from '~/generated/proto/leapmux/v1/auth_pb'
import { ChannelService } from '~/generated/proto/leapmux/v1/channel_pb'
import { SectionService } from '~/generated/proto/leapmux/v1/section_pb'
import { UserCRDT } from '~/generated/proto/leapmux/v1/user_ops_pb'
import { UserService } from '~/generated/proto/leapmux/v1/user_pb'
import { WorkerManagementService } from '~/generated/proto/leapmux/v1/worker_pb'
import { WorkspaceService } from '~/generated/proto/leapmux/v1/workspace_pb'
import { transport, unloadTransport } from './transport'

export const appClient = createClient(AppService, transport)
export const authClient = createClient(AuthService, transport)
export const workerClient = createClient(WorkerManagementService, transport)
export const userClient = createClient(UserService, transport)
export const sectionClient = createClient(SectionService, transport)
export const channelClient = createClient(ChannelService, transport)
export const workspaceClient = createClient(WorkspaceService, transport)
export const userCRDTClient = createClient(UserCRDT, transport)
export const adminSettingsClient = createClient(AdminSettingsService, transport)
export const adminUserClient = createClient(AdminUserService, transport)
/**
 * SubmitOps over the keepalive transport, for the `pagehide` flush only. See
 * `unloadTransport` for why keepalive is what makes that flush reach the hub at
 * all, and for the 64 KiB budget it carries.
 */
export const userCRDTUnloadClient = createClient(UserCRDT, unloadTransport)
