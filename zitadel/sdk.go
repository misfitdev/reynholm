package zitadel

import (
	"context"
	"fmt"

	zclient "github.com/zitadel/zitadel-go/v3/pkg/client"
	authorizationV2 "github.com/zitadel/zitadel-go/v3/pkg/client/zitadel/authorization/v2"
	filterV2 "github.com/zitadel/zitadel-go/v3/pkg/client/zitadel/filter/v2"
	objectV2 "github.com/zitadel/zitadel-go/v3/pkg/client/zitadel/object/v2"
	projectV2 "github.com/zitadel/zitadel-go/v3/pkg/client/zitadel/project/v2"
	userV2 "github.com/zitadel/zitadel-go/v3/pkg/client/zitadel/user/v2"
)

// sdkServices adapts the ZITADEL v3 unified Client to the services
// interface. Each method maps a Client helper onto its underlying v2 gRPC
// call, normalising pagination into (results, totalResult, error).
type sdkServices struct {
	c *zclient.Client
}

func (s *sdkServices) ListUsersPage(ctx context.Context, offset uint64, limit uint32) ([]*userV2.User, uint64, error) {
	resp, err := s.c.UserServiceV2().ListUsers(ctx, &userV2.ListUsersRequest{
		Query: &objectV2.ListQuery{
			Offset: offset,
			Limit:  limit,
			Asc:    true,
		},
	})
	if err != nil {
		return nil, 0, err
	}
	return resp.GetResult(), resp.GetDetails().GetTotalResult(), nil
}

func (s *sdkServices) ListProjectRolesPage(ctx context.Context, projectID string, offset uint64, limit uint32) ([]*projectV2.ProjectRole, uint64, error) {
	resp, err := s.c.ProjectServiceV2().ListProjectRoles(ctx, &projectV2.ListProjectRolesRequest{
		ProjectId: projectID,
		Pagination: &filterV2.PaginationRequest{
			Offset: offset,
			Limit:  limit,
			Asc:    true,
		},
	})
	if err != nil {
		return nil, 0, err
	}
	return resp.GetProjectRoles(), resp.GetPagination().GetTotalResult(), nil
}

func (s *sdkServices) AddProjectRole(ctx context.Context, projectID, roleKey, displayName, group string) error {
	req := &projectV2.AddProjectRoleRequest{
		ProjectId:   projectID,
		RoleKey:     roleKey,
		DisplayName: displayName,
	}
	if group != "" {
		req.Group = &group
	}
	if _, err := s.c.ProjectServiceV2().AddProjectRole(ctx, req); err != nil {
		return fmt.Errorf("zitadel: add project role %q on %q: %w", roleKey, projectID, err)
	}
	return nil
}

func (s *sdkServices) RemoveProjectRole(ctx context.Context, projectID, roleKey string) error {
	_, err := s.c.ProjectServiceV2().RemoveProjectRole(ctx, &projectV2.RemoveProjectRoleRequest{
		ProjectId: projectID,
		RoleKey:   roleKey,
	})
	if err != nil {
		return fmt.Errorf("zitadel: remove project role %q on %q: %w", roleKey, projectID, err)
	}
	return nil
}

func (s *sdkServices) ListAuthorizationsPage(ctx context.Context, projectID string, offset uint64, limit uint32) ([]*authorizationV2.Authorization, uint64, error) {
	resp, err := s.c.AuthorizationServiceV2().ListAuthorizations(ctx, &authorizationV2.ListAuthorizationsRequest{
		Pagination: &filterV2.PaginationRequest{
			Offset: offset,
			Limit:  limit,
			Asc:    true,
		},
		Filters: []*authorizationV2.AuthorizationsSearchFilter{
			{
				Filter: &authorizationV2.AuthorizationsSearchFilter_ProjectId{
					ProjectId: &filterV2.IDFilter{Id: projectID},
				},
			},
		},
	})
	if err != nil {
		return nil, 0, err
	}
	return resp.GetAuthorizations(), resp.GetPagination().GetTotalResult(), nil
}

func (s *sdkServices) CreateAuthorization(ctx context.Context, orgID, projectID, userID, roleKey string) error {
	_, err := s.c.AuthorizationServiceV2().CreateAuthorization(ctx, &authorizationV2.CreateAuthorizationRequest{
		UserId:         userID,
		ProjectId:      projectID,
		OrganizationId: orgID,
		RoleKeys:       []string{roleKey},
	})
	if err != nil {
		return fmt.Errorf("zitadel: create authorization (user=%q project=%q role=%q): %w", userID, projectID, roleKey, err)
	}
	return nil
}

func (s *sdkServices) DeleteAuthorization(ctx context.Context, authorizationID string) error {
	_, err := s.c.AuthorizationServiceV2().DeleteAuthorization(ctx, &authorizationV2.DeleteAuthorizationRequest{
		Id: authorizationID,
	})
	if err != nil {
		return fmt.Errorf("zitadel: delete authorization %q: %w", authorizationID, err)
	}
	return nil
}
