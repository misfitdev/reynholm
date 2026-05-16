// Package zitadel wraps the ZITADEL v3 unified client for the narrow slice
// of functionality reynholm needs: translating Workspace emails to ZITADEL
// user IDs, reading and managing project roles, and reading and managing
// user authorizations (grants) for a single project.
//
// All mutating helpers operate exclusively on roles whose `Group` field
// matches the configured `managedGroup` string. Callers are responsible for
// filtering grants to that managed set; this package only exposes the
// primitives.
//
// The SDK seam is the unexported [services] interface, allowing tests to
// substitute a fake without spinning up gRPC.
package zitadel

import (
	"context"
	"errors"
	"fmt"
	"strings"

	zclient "github.com/zitadel/zitadel-go/v3/pkg/client"
	authorizationV2 "github.com/zitadel/zitadel-go/v3/pkg/client/zitadel/authorization/v2"
	projectV2 "github.com/zitadel/zitadel-go/v3/pkg/client/zitadel/project/v2"
	userV2 "github.com/zitadel/zitadel-go/v3/pkg/client/zitadel/user/v2"
	"github.com/zitadel/zitadel-go/v3/pkg/zitadel"
)

// pageSize is the per-call limit used for paginated reads. The ZITADEL v2
// list endpoints cap pagination at 1000 server-side; 100 is the default and
// a safe, network-friendly value for the modest result sets reynholm sees.
const pageSize uint32 = 100

// Grant represents a single user authorization (project grant) for a role.
// AuthorizationID is the opaque identifier needed to revoke the grant.
type Grant struct {
	UserID          string
	RoleKey         string
	AuthorizationID string
}

// services is the subset of the ZITADEL unified client used by Client. The
// page-shaped methods exist so tests can simulate pagination directly.
type services interface {
	ListUsersPage(ctx context.Context, offset uint64, limit uint32) (users []*userV2.User, total uint64, err error)
	ListProjectRolesPage(ctx context.Context, projectID string, offset uint64, limit uint32) (roles []*projectV2.ProjectRole, total uint64, err error)
	AddProjectRole(ctx context.Context, projectID, roleKey, displayName, group string) error
	RemoveProjectRole(ctx context.Context, projectID, roleKey string) error
	ListAuthorizationsPage(ctx context.Context, projectID string, offset uint64, limit uint32) (auths []*authorizationV2.Authorization, total uint64, err error)
	CreateAuthorization(ctx context.Context, projectID, userID, roleKey string) error
	DeleteAuthorization(ctx context.Context, authorizationID string) error
}

// Client is the high-level ZITADEL client used by reynholm.
type Client struct {
	svc services
}

// NewClient constructs a Client backed by the ZITADEL v3 unified gRPC client
// authenticated with a Personal Access Token. domain is the bare ZITADEL
// hostname (e.g. "your-instance.zitadel.cloud"); TLS on 443 is assumed.
func NewClient(ctx context.Context, domain, pat string) (*Client, error) {
	if domain == "" {
		return nil, errors.New("zitadel: domain is required")
	}
	if pat == "" {
		return nil, errors.New("zitadel: pat is required")
	}
	z := zitadel.New(domain)
	c, err := zclient.New(ctx, z, zclient.WithAuth(zclient.PAT(pat)))
	if err != nil {
		return nil, fmt.Errorf("zitadel: client.New: %w", err)
	}
	return &Client{svc: &sdkServices{c: c}}, nil
}

// NewClientWithServices builds a Client around a caller-supplied services
// implementation. This is the seam used by tests; production callers should
// use NewClient.
func NewClientWithServices(s services) *Client {
	return &Client{svc: s}
}

// LookupUserIDs fetches every ZITADEL user (paginated) and returns a map
// from lowercased primary email to UserID. Users without a Human profile or
// without an email are dropped silently. On duplicate emails (which would
// indicate a misconfigured tenant), the first occurrence wins.
func (c *Client) LookupUserIDs(ctx context.Context) (map[string]string, error) {
	if c == nil || c.svc == nil {
		return nil, errors.New("zitadel: client not initialized")
	}
	out := make(map[string]string)
	var offset uint64
	for {
		users, total, err := c.svc.ListUsersPage(ctx, offset, pageSize)
		if err != nil {
			return nil, fmt.Errorf("zitadel: list users: %w", err)
		}
		for _, u := range users {
			if u == nil {
				continue
			}
			human := u.GetHuman()
			if human == nil {
				continue
			}
			email := human.GetEmail().GetEmail()
			if email == "" {
				continue
			}
			key := strings.ToLower(email)
			if _, dup := out[key]; dup {
				continue
			}
			out[key] = u.GetUserId()
		}
		offset += uint64(len(users))
		if len(users) == 0 || offset >= total {
			return out, nil
		}
	}
}

// ListProjectRoles fetches every project role on projectID (paginated) and
// returns a map RoleKey -> DisplayName, restricted to roles whose Group
// matches managedGroup exactly. Roles outside the managed group are ignored:
// they represent state reynholm does not own and must not touch.
func (c *Client) ListProjectRoles(ctx context.Context, projectID, managedGroup string) (map[string]string, error) {
	if c == nil || c.svc == nil {
		return nil, errors.New("zitadel: client not initialized")
	}
	if projectID == "" {
		return nil, errors.New("zitadel: projectID is required")
	}
	out := make(map[string]string)
	var offset uint64
	for {
		roles, total, err := c.svc.ListProjectRolesPage(ctx, projectID, offset, pageSize)
		if err != nil {
			return nil, fmt.Errorf("zitadel: list project roles %q: %w", projectID, err)
		}
		for _, r := range roles {
			if r == nil {
				continue
			}
			if r.GetGroup() != managedGroup {
				continue
			}
			out[r.GetKey()] = r.GetDisplayName()
		}
		offset += uint64(len(roles))
		if len(roles) == 0 || offset >= total {
			return out, nil
		}
	}
}

// AddProjectRole creates a new role on projectID, tagging it with the
// managedGroup string so subsequent reconciliations recognize it as managed.
func (c *Client) AddProjectRole(ctx context.Context, projectID, roleKey, displayName, managedGroup string) error {
	if c == nil || c.svc == nil {
		return errors.New("zitadel: client not initialized")
	}
	if projectID == "" || roleKey == "" {
		return errors.New("zitadel: projectID and roleKey are required")
	}
	return c.svc.AddProjectRole(ctx, projectID, roleKey, displayName, managedGroup)
}

// RemoveProjectRole deletes a role from projectID by its key.
func (c *Client) RemoveProjectRole(ctx context.Context, projectID, roleKey string) error {
	if c == nil || c.svc == nil {
		return errors.New("zitadel: client not initialized")
	}
	if projectID == "" || roleKey == "" {
		return errors.New("zitadel: projectID and roleKey are required")
	}
	return c.svc.RemoveProjectRole(ctx, projectID, roleKey)
}

// ListUserGrants enumerates every authorization on projectID (paginated)
// and flattens them into per-(user, role) Grant entries. A single ZITADEL
// authorization may hold multiple role keys; each becomes its own Grant so
// callers can diff role-by-role. AuthorizationID is preserved on every Grant
// for later DeleteAuthorization calls.
func (c *Client) ListUserGrants(ctx context.Context, projectID string) ([]Grant, error) {
	if c == nil || c.svc == nil {
		return nil, errors.New("zitadel: client not initialized")
	}
	if projectID == "" {
		return nil, errors.New("zitadel: projectID is required")
	}
	var out []Grant
	var offset uint64
	for {
		auths, total, err := c.svc.ListAuthorizationsPage(ctx, projectID, offset, pageSize)
		if err != nil {
			return nil, fmt.Errorf("zitadel: list authorizations %q: %w", projectID, err)
		}
		for _, a := range auths {
			if a == nil {
				continue
			}
			userID := a.GetUser().GetId()
			authID := a.GetId()
			if userID == "" || authID == "" {
				continue
			}
			for _, r := range a.GetRoles() {
				if r == nil || r.GetKey() == "" {
					continue
				}
				out = append(out, Grant{
					UserID:          userID,
					RoleKey:         r.GetKey(),
					AuthorizationID: authID,
				})
			}
		}
		offset += uint64(len(auths))
		if len(auths) == 0 || offset >= total {
			return out, nil
		}
	}
}

// AddUserGrant creates a new authorization granting roleKey on projectID to
// userID. Callers handle batching: each call creates exactly one
// authorization carrying exactly one role.
func (c *Client) AddUserGrant(ctx context.Context, projectID, userID, roleKey string) error {
	if c == nil || c.svc == nil {
		return errors.New("zitadel: client not initialized")
	}
	if projectID == "" || userID == "" || roleKey == "" {
		return errors.New("zitadel: projectID, userID and roleKey are required")
	}
	return c.svc.CreateAuthorization(ctx, projectID, userID, roleKey)
}

// RemoveUserGrant deletes the authorization identified by authorizationID.
// projectID, userID, and roleKey are accepted for symmetry and logging but
// are not sent to the API: ZITADEL DeleteAuthorization keys solely on the
// authorization ID returned by ListUserGrants.
func (c *Client) RemoveUserGrant(ctx context.Context, projectID, userID, roleKey, authorizationID string) error {
	if c == nil || c.svc == nil {
		return errors.New("zitadel: client not initialized")
	}
	if authorizationID == "" {
		return errors.New("zitadel: authorizationID is required")
	}
	_ = projectID
	_ = userID
	_ = roleKey
	return c.svc.DeleteAuthorization(ctx, authorizationID)
}
