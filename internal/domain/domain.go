// Package domain contains Knotwork's core entities and state constants.
//
// The package has no dependencies on storage or transport so adapters can be
// swapped without cross-domain imports (see docs/ARCHITECTURE.md).
package domain

const (
	RoleAdmin    = "admin"
	RoleMember   = "member"
	RoleReadOnly = "read_only"
)

const (
	PipelinePendingApproval = "pending_approval"
	PipelineQueued          = "queued"
	PipelineRunning         = "running"
	PipelineSuccess         = "success"
	PipelineFailed          = "failed"
)

const (
	PeerAllowlisted = "allowlisted"
	PeerPending     = "pending"
	PeerBlocked     = "blocked"
)

const (
	MRStateOpen   = "open"
	MRStateMerged = "merged"
	MRStateClosed = "closed"
)

type User struct {
	ID          string `json:"id"`
	ExternalID  string `json:"-"`
	SCIMID      string `json:"-"`
	Username    string `json:"username"`
	Email       string `json:"email"`
	DisplayName string `json:"display_name"`
	Role        string `json:"role"`
	IsActive    bool   `json:"active"`
	PublicKey   string `json:"public_key"`
	CreatedAt   string `json:"created_at"`
	UpdatedAt   string `json:"updated_at"`
}

func (u User) IsAdmin() bool { return u.Role == RoleAdmin }

type Repository struct {
	ID                string `json:"id"`
	OwnerID           string `json:"owner_id"`
	Name              string `json:"name"`
	Description       string `json:"description"`
	Path              string `json:"path"`
	DiskPath          string `json:"disk_path"`
	IsPrivate         bool   `json:"private"`
	DefaultBranch     string `json:"default_branch"`
	ProtectedBranches string `json:"protected_branches"`
	LastBundleAt      string `json:"last_bundle_at"`
	CreatedAt         string `json:"created_at"`
	UpdatedAt         string `json:"updated_at"`
}

type MergeRequest struct {
	ID             string `json:"id"`
	RepoID         string `json:"repo_id"`
	Number         int64  `json:"number"`
	AuthorID       string `json:"author_id"`
	SourceRepoURI  string `json:"source_repo_uri"`
	SourceBranch   string `json:"source_branch"`
	TargetBranch   string `json:"target_branch"`
	Title          string `json:"title"`
	Description    string `json:"description"`
	State          string `json:"state"`
	IsFederated    bool   `json:"federated"`
	FederatedActor string `json:"federated_actor_id"`
	OriginDomain   string `json:"origin_domain"`
	HeadSHA        string `json:"head_sha"`
	CreatedAt      string `json:"created_at"`
	UpdatedAt      string `json:"updated_at"`
}

type Pipeline struct {
	ID               string `json:"id"`
	RepoID           string `json:"repo_id"`
	MRID             string `json:"mr_id"`
	CommitSHA        string `json:"commit_sha"`
	RefName          string `json:"ref_name"`
	Status           string `json:"status"`
	RequiresApproval bool   `json:"requires_approval"`
	ApprovedBy       string `json:"approved_by"`
	ApprovedAt       string `json:"approved_at"`
	CreatedAt        string `json:"created_at"`
	FinishedAt       string `json:"finished_at"`
}

type Peer struct {
	ID               string `json:"id"`
	Domain           string `json:"domain"`
	InboxURL         string `json:"inbox_url"`
	ActorURL         string `json:"actor_url"`
	PublicKeyPEM     string `json:"public_key_pem"`
	TrustStatus      string `json:"trust_status"`
	AllowFederatedCI bool   `json:"allow_federated_ci"`
	LastVerifiedAt   string `json:"last_verified_at"`
	CreatedAt        string `json:"created_at"`
}
