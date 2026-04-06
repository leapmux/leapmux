package mongodb

import (
	"github.com/leapmux/leapmux/internal/hub/store"
)

// Sub-store type declarations. Each is implemented in its own file.
type orgStore struct{ s *mongoStore }
type userStore struct{ s *mongoStore }
type sessionStore struct{ s *mongoStore }
type orgMemberStore struct{ s *mongoStore }
type workerStore struct{ s *mongoStore }
type workerAccessGrantStore struct{ s *mongoStore }
type workerNotificationStore struct{ s *mongoStore }
type registrationStore struct{ s *mongoStore }
type workspaceStore struct{ s *mongoStore }
type workspaceAccessStore struct{ s *mongoStore }
type workspaceTabStore struct{ s *mongoStore }
type workspaceLayoutStore struct{ s *mongoStore }
type workspaceSectionStore struct{ s *mongoStore }
type workspaceSectionItemStore struct{ s *mongoStore }
type oauthProviderStore struct{ s *mongoStore }
type oauthStateStore struct{ s *mongoStore }
type oauthTokenStore struct{ s *mongoStore }
type oauthUserLinkStore struct{ s *mongoStore }
type pendingOAuthSignupStore struct{ s *mongoStore }
type cleanupStore struct{ s *mongoStore }

var _ store.OrgStore = (*orgStore)(nil)
var _ store.UserStore = (*userStore)(nil)
var _ store.SessionStore = (*sessionStore)(nil)
var _ store.OrgMemberStore = (*orgMemberStore)(nil)
var _ store.WorkerStore = (*workerStore)(nil)
var _ store.WorkerAccessGrantStore = (*workerAccessGrantStore)(nil)
var _ store.WorkerNotificationStore = (*workerNotificationStore)(nil)
var _ store.RegistrationStore = (*registrationStore)(nil)

var _ store.WorkspaceStore = (*workspaceStore)(nil)
var _ store.WorkspaceAccessStore = (*workspaceAccessStore)(nil)
var _ store.WorkspaceTabStore = (*workspaceTabStore)(nil)
var _ store.WorkspaceLayoutStore = (*workspaceLayoutStore)(nil)
var _ store.WorkspaceSectionStore = (*workspaceSectionStore)(nil)
var _ store.WorkspaceSectionItemStore = (*workspaceSectionItemStore)(nil)

var _ store.OAuthProviderStore = (*oauthProviderStore)(nil)
var _ store.OAuthStateStore = (*oauthStateStore)(nil)
var _ store.OAuthTokenStore = (*oauthTokenStore)(nil)
var _ store.OAuthUserLinkStore = (*oauthUserLinkStore)(nil)
var _ store.PendingOAuthSignupStore = (*pendingOAuthSignupStore)(nil)
var _ store.CleanupStore = (*cleanupStore)(nil)
