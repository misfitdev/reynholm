package google

import (
	"context"

	admin "google.golang.org/api/admin/directory/v1"
)

// sdkDirectory adapts *admin.Service to the Directory interface.
type sdkDirectory struct {
	svc *admin.Service
}

func (s *sdkDirectory) GetGroup(ctx context.Context, groupKey string) (*admin.Group, error) {
	return s.svc.Groups.Get(groupKey).Context(ctx).Do()
}

func (s *sdkDirectory) ListMembersPage(ctx context.Context, groupKey, pageToken string) ([]*admin.Member, string, error) {
	call := s.svc.Members.List(groupKey).Context(ctx)
	if pageToken != "" {
		call = call.PageToken(pageToken)
	}
	resp, err := call.Do()
	if err != nil {
		return nil, "", err
	}
	return resp.Members, resp.NextPageToken, nil
}

func (s *sdkDirectory) ListGroupsPage(ctx context.Context, domain, query, pageToken string) ([]*admin.Group, string, error) {
	call := s.svc.Groups.List().Domain(domain).Context(ctx)
	if query != "" {
		call = call.Query(query)
	}
	if pageToken != "" {
		call = call.PageToken(pageToken)
	}
	resp, err := call.Do()
	if err != nil {
		return nil, "", err
	}
	return resp.Groups, resp.NextPageToken, nil
}
