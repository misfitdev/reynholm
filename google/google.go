// Package google wraps the Google Admin SDK Directory API for the narrow
// slice of functionality reynholm needs: resolving group display names and
// listing the flattened, active, lowercase-normalized members of a group.
//
// Authentication uses domain-wide delegation via a service account that
// impersonates a real admin user (Subject). The service account email comes
// from GOOGLE_SERVICE_ACCOUNT_EMAIL; the admin to impersonate comes from
// GOOGLE_ADMIN_EMAIL. Application Default Credentials supply the underlying
// credentials for the impersonation request.
package google

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"

	admin "google.golang.org/api/admin/directory/v1"
	"google.golang.org/api/impersonate"
	"google.golang.org/api/option"
)

const (
	envServiceAccount = "GOOGLE_SERVICE_ACCOUNT_EMAIL"
	envAdminEmail     = "GOOGLE_ADMIN_EMAIL"

	memberTypeGroup    = "GROUP"
	memberStatusActive = "ACTIVE"
)

// Directory is the subset of the Admin SDK Directory API used by Client.
type Directory interface {
	GetGroup(ctx context.Context, groupKey string) (*admin.Group, error)
	// ListMembersPage returns one page of members. Empty pageToken requests
	// the first page; empty returned token marks the last page.
	ListMembersPage(ctx context.Context, groupKey, pageToken string) (members []*admin.Member, nextPageToken string, err error)
	// ListGroupsPage returns one page of groups matching query. The query
	// uses the Directory API search syntax (e.g. "email:access-*").
	ListGroupsPage(ctx context.Context, domain, query, pageToken string) (groups []*admin.Group, nextPageToken string, err error)
}

// Client is the high-level Google Workspace client used by reynholm.
type Client struct {
	dir Directory
}

// NewClient constructs a Client using domain-wide delegation. The service
// account email and admin subject email are read from environment variables.
func NewClient(ctx context.Context) (*Client, error) {
	sa := os.Getenv(envServiceAccount)
	if sa == "" {
		return nil, fmt.Errorf("google: %s is required", envServiceAccount)
	}
	subject := os.Getenv(envAdminEmail)
	if subject == "" {
		return nil, fmt.Errorf("google: %s is required", envAdminEmail)
	}

	ts, err := impersonate.CredentialsTokenSource(ctx, impersonate.CredentialsConfig{
		TargetPrincipal: sa,
		Scopes: []string{
			admin.AdminDirectoryGroupReadonlyScope,
			admin.AdminDirectoryGroupMemberReadonlyScope,
		},
		Subject: subject,
	})
	if err != nil {
		return nil, fmt.Errorf("google: impersonate token source: %w", err)
	}

	svc, err := admin.NewService(ctx, option.WithTokenSource(ts))
	if err != nil {
		return nil, fmt.Errorf("google: admin.NewService: %w", err)
	}

	return &Client{dir: &sdkDirectory{svc: svc}}, nil
}

// NewClientWithDirectory builds a Client around a caller-supplied Directory.
// This is the seam used by tests; production callers should use NewClient.
func NewClientWithDirectory(d Directory) *Client {
	return &Client{dir: d}
}

// GetGroupDisplayName returns the human-readable name of the group identified
// by groupKey (email or ID). If the group has no display name set, the
// returned string is empty and the error is nil.
func (c *Client) GetGroupDisplayName(ctx context.Context, groupKey string) (string, error) {
	if c == nil || c.dir == nil {
		return "", errors.New("google: client not initialized")
	}
	if groupKey == "" {
		return "", errors.New("google: groupKey is required")
	}
	g, err := c.dir.GetGroup(ctx, groupKey)
	if err != nil {
		return "", fmt.Errorf("google: get group %q: %w", groupKey, err)
	}
	return g.Name, nil
}

// ListMembers returns a sorted-unique, lowercased list of active member
// emails for groupKey. Nested groups (members with Type == "GROUP") are
// recursively expanded and merged in. Members that are not active or that
// lack an email are dropped.
func (c *Client) ListMembers(ctx context.Context, groupKey string) ([]string, error) {
	if c == nil || c.dir == nil {
		return nil, errors.New("google: client not initialized")
	}
	if groupKey == "" {
		return nil, errors.New("google: groupKey is required")
	}
	seen := make(map[string]struct{}) // nosemgrep: trailofbits.go.iterate-over-empty-map.iterate-over-empty-map
	visited := make(map[string]struct{})
	if err := c.collect(ctx, groupKey, seen, visited); err != nil {
		return nil, err
	}
	out := make([]string, 0, len(seen))
	for e := range seen {
		out = append(out, e)
	}
	sort.Strings(out)
	return out, nil
}

func (c *Client) collect(ctx context.Context, groupKey string, seen, visited map[string]struct{}) error {
	// Cycle guard: Workspace groups can reference each other in cycles.
	key := strings.ToLower(groupKey)
	if _, dup := visited[key]; dup {
		return nil
	}
	visited[key] = struct{}{}

	var pageToken string
	for {
		members, next, err := c.dir.ListMembersPage(ctx, groupKey, pageToken)
		if err != nil {
			return fmt.Errorf("google: list members %q: %w", groupKey, err)
		}
		for _, m := range members {
			if m == nil {
				continue
			}
			if strings.EqualFold(m.Type, memberTypeGroup) {
				nested := m.Email
				if nested == "" {
					nested = m.Id
				}
				if nested == "" {
					continue
				}
				if err := c.collect(ctx, nested, seen, visited); err != nil {
					return err
				}
				continue
			}
			if !isActive(m) {
				continue
			}
			if m.Email == "" {
				continue
			}
			seen[strings.ToLower(m.Email)] = struct{}{}
		}
		if next == "" {
			return nil
		}
		pageToken = next
	}
}

// isActive treats unknown/empty Status as inactive: we will not grant access
// to a member whose state we cannot positively confirm.
func isActive(m *admin.Member) bool {
	return strings.EqualFold(m.Status, memberStatusActive)
}

// ListGroups returns all group emails in domain matching query. The query
// uses the Directory API search syntax (e.g. "email:access-*"). Results are
// sorted and lowercased.
func (c *Client) ListGroups(ctx context.Context, domain, query string) ([]string, error) {
	if c == nil || c.dir == nil {
		return nil, errors.New("google: client not initialized")
	}
	if domain == "" {
		return nil, errors.New("google: domain is required")
	}

	var (
		emails    []string
		pageToken string
	)
	for {
		groups, next, err := c.dir.ListGroupsPage(ctx, domain, query, pageToken)
		if err != nil {
			return nil, fmt.Errorf("google: list groups: %w", err)
		}
		for _, g := range groups {
			if g == nil || g.Email == "" {
				continue
			}
			emails = append(emails, strings.ToLower(g.Email))
		}
		if next == "" {
			break
		}
		pageToken = next
	}
	sort.Strings(emails)
	return emails, nil
}
