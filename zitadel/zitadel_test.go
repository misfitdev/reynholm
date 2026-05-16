package zitadel

import (
	"context"
	"errors"
	"reflect"
	"sort"
	"strconv"
	"testing"

	authorizationV2 "github.com/zitadel/zitadel-go/v3/pkg/client/zitadel/authorization/v2"
	projectV2 "github.com/zitadel/zitadel-go/v3/pkg/client/zitadel/project/v2"
	userV2 "github.com/zitadel/zitadel-go/v3/pkg/client/zitadel/user/v2"
)

// fakeServices honours offset/limit the same way the real ZITADEL API does
// so the pagination loops in Client get exercised end-to-end.
type fakeServices struct {
	users           []*userV2.User
	roles           map[string][]*projectV2.ProjectRole
	auths           map[string][]*authorizationV2.Authorization
	listErr         error
	addRoleCalls    []addRoleCall
	removeRoleCalls []removeRoleCall
	createAuthCalls []createAuthCall
	deleteAuthCalls []string
	addErr          error
	removeErr       error
	createErr       error
	deleteErr       error
}

type addRoleCall struct {
	ProjectID, RoleKey, DisplayName, Group string
}
type removeRoleCall struct {
	ProjectID, RoleKey string
}
type createAuthCall struct {
	ProjectID, UserID, RoleKey string
}

func newFakeServices() *fakeServices {
	return &fakeServices{
		roles: map[string][]*projectV2.ProjectRole{},
		auths: map[string][]*authorizationV2.Authorization{},
	}
}

func (f *fakeServices) ListUsersPage(_ context.Context, offset uint64, limit uint32) ([]*userV2.User, uint64, error) {
	if f.listErr != nil {
		return nil, 0, f.listErr
	}
	return slicePage(f.users, offset, limit), uint64(len(f.users)), nil
}

func (f *fakeServices) ListProjectRolesPage(_ context.Context, projectID string, offset uint64, limit uint32) ([]*projectV2.ProjectRole, uint64, error) {
	if f.listErr != nil {
		return nil, 0, f.listErr
	}
	all := f.roles[projectID]
	return slicePage(all, offset, limit), uint64(len(all)), nil
}

func (f *fakeServices) AddProjectRole(_ context.Context, projectID, roleKey, displayName, group string) error {
	if f.addErr != nil {
		return f.addErr
	}
	f.addRoleCalls = append(f.addRoleCalls, addRoleCall{projectID, roleKey, displayName, group})
	return nil
}

func (f *fakeServices) RemoveProjectRole(_ context.Context, projectID, roleKey string) error {
	if f.removeErr != nil {
		return f.removeErr
	}
	f.removeRoleCalls = append(f.removeRoleCalls, removeRoleCall{projectID, roleKey})
	return nil
}

func (f *fakeServices) ListAuthorizationsPage(_ context.Context, projectID string, offset uint64, limit uint32) ([]*authorizationV2.Authorization, uint64, error) {
	if f.listErr != nil {
		return nil, 0, f.listErr
	}
	all := f.auths[projectID]
	return slicePage(all, offset, limit), uint64(len(all)), nil
}

func (f *fakeServices) CreateAuthorization(_ context.Context, projectID, userID, roleKey string) error {
	if f.createErr != nil {
		return f.createErr
	}
	f.createAuthCalls = append(f.createAuthCalls, createAuthCall{projectID, userID, roleKey})
	return nil
}

func (f *fakeServices) DeleteAuthorization(_ context.Context, authorizationID string) error {
	if f.deleteErr != nil {
		return f.deleteErr
	}
	f.deleteAuthCalls = append(f.deleteAuthCalls, authorizationID)
	return nil
}

// slicePage mimics offset/limit pagination over an in-memory slice.
func slicePage[T any](all []T, offset uint64, limit uint32) []T {
	if offset >= uint64(len(all)) {
		return nil
	}
	end := offset + uint64(limit)
	if end > uint64(len(all)) {
		end = uint64(len(all))
	}
	return all[offset:end]
}
func humanUser(id, email string) *userV2.User {
	return &userV2.User{
		UserId: id,
		Type: &userV2.User_Human{
			Human: &userV2.HumanUser{
				Email: &userV2.HumanEmail{Email: email},
			},
		},
	}
}

func machineUser(id string) *userV2.User {
	return &userV2.User{
		UserId: id,
		Type:   &userV2.User_Machine{Machine: &userV2.MachineUser{}},
	}
}

func TestLookupUserIDs_LowercaseAndSkipNonHuman(t *testing.T) {
	f := newFakeServices()
	f.users = []*userV2.User{
		humanUser("u1", "Alice@Example.com"),
		humanUser("u2", "BOB@example.com"),
		machineUser("m1"),
		{UserId: "u3", Type: &userV2.User_Human{Human: &userV2.HumanUser{}}},
		humanUser("u4", "carol@example.com"),
	}
	c := NewClientWithServices(f)

	got, err := c.LookupUserIDs(context.Background())
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	want := map[string]string{
		"alice@example.com": "u1",
		"bob@example.com":   "u2",
		"carol@example.com": "u4",
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("LookupUserIDs = %v, want %v", got, want)
	}
}

func TestLookupUserIDs_Pagination(t *testing.T) {
	f := newFakeServices()
	for i := 0; i < 250; i++ {
		f.users = append(f.users, humanUser(
			"u"+strconv.Itoa(i),
			"user"+strconv.Itoa(i)+"@example.com",
		))
	}
	c := NewClientWithServices(f)

	got, err := c.LookupUserIDs(context.Background())
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if len(got) != 250 {
		t.Errorf("got %d users, want 250", len(got))
	}
}

func TestLookupUserIDs_DuplicateEmailFirstWins(t *testing.T) {
	f := newFakeServices()
	f.users = []*userV2.User{
		humanUser("first", "dup@example.com"),
		humanUser("second", "DUP@example.com"),
	}
	c := NewClientWithServices(f)

	got, err := c.LookupUserIDs(context.Background())
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if got["dup@example.com"] != "first" {
		t.Errorf("dup resolution = %q, want first", got["dup@example.com"])
	}
}

func TestLookupUserIDs_ErrorPropagated(t *testing.T) {
	f := newFakeServices()
	f.listErr = errors.New("boom")
	c := NewClientWithServices(f)

	if _, err := c.LookupUserIDs(context.Background()); err == nil {
		t.Fatal("expected error")
	}
}

func projectRole(key, displayName, group string) *projectV2.ProjectRole {
	return &projectV2.ProjectRole{
		Key:         key,
		DisplayName: displayName,
		Group:       group,
	}
}

func TestListProjectRoles_FiltersByManagedGroup(t *testing.T) {
	f := newFakeServices()
	f.roles["p1"] = []*projectV2.ProjectRole{
		projectRole("engineers", "Engineering", "google-sync"),
		projectRole("admins", "Admins", "google-sync"),
		projectRole("legacy", "Legacy", "other-source"),
		projectRole("orphan", "Orphan", ""),
	}
	c := NewClientWithServices(f)

	got, err := c.ListProjectRoles(context.Background(), "p1", "google-sync")
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	want := map[string]string{
		"engineers": "Engineering",
		"admins":    "Admins",
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("ListProjectRoles = %v, want %v", got, want)
	}
}

func TestListProjectRoles_RequiresProjectID(t *testing.T) {
	c := NewClientWithServices(newFakeServices())
	if _, err := c.ListProjectRoles(context.Background(), "", "g"); err == nil {
		t.Fatal("expected error for empty projectID")
	}
}

func TestAddProjectRole_PassesGroupTag(t *testing.T) {
	f := newFakeServices()
	c := NewClientWithServices(f)

	if err := c.AddProjectRole(context.Background(), "p1", "engineers", "Engineering", "google-sync"); err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	want := []addRoleCall{{"p1", "engineers", "Engineering", "google-sync"}}
	if !reflect.DeepEqual(f.addRoleCalls, want) {
		t.Errorf("addRoleCalls = %v, want %v", f.addRoleCalls, want)
	}
}

func TestAddProjectRole_RejectsEmptyArgs(t *testing.T) {
	c := NewClientWithServices(newFakeServices())
	if err := c.AddProjectRole(context.Background(), "", "k", "n", "g"); err == nil {
		t.Fatal("expected error for empty projectID")
	}
	if err := c.AddProjectRole(context.Background(), "p", "", "n", "g"); err == nil {
		t.Fatal("expected error for empty roleKey")
	}
}

func TestRemoveProjectRole(t *testing.T) {
	f := newFakeServices()
	c := NewClientWithServices(f)
	if err := c.RemoveProjectRole(context.Background(), "p1", "engineers"); err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	want := []removeRoleCall{{"p1", "engineers"}}
	if !reflect.DeepEqual(f.removeRoleCalls, want) {
		t.Errorf("removeRoleCalls = %v, want %v", f.removeRoleCalls, want)
	}
}

func authWith(id, userID string, roleKeys ...string) *authorizationV2.Authorization {
	roles := make([]*authorizationV2.Role, 0, len(roleKeys))
	for _, k := range roleKeys {
		roles = append(roles, &authorizationV2.Role{Key: k})
	}
	return &authorizationV2.Authorization{
		Id:    id,
		User:  &authorizationV2.User{Id: userID},
		Roles: roles,
	}
}

func TestListUserGrants_FlattensRoles(t *testing.T) {
	f := newFakeServices()
	f.auths["p1"] = []*authorizationV2.Authorization{
		authWith("a1", "u1", "engineers", "admins"),
		authWith("a2", "u2", "engineers"),
		authWith("a3", "", "skipped"),
		{Id: "a4", User: &authorizationV2.User{Id: "u3"}, Roles: []*authorizationV2.Role{{Key: ""}}},
	}
	c := NewClientWithServices(f)

	got, err := c.ListUserGrants(context.Background(), "p1")
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	sort.Slice(got, func(i, j int) bool {
		if got[i].UserID != got[j].UserID {
			return got[i].UserID < got[j].UserID
		}
		return got[i].RoleKey < got[j].RoleKey
	})
	want := []Grant{
		{UserID: "u1", RoleKey: "admins", AuthorizationID: "a1"},
		{UserID: "u1", RoleKey: "engineers", AuthorizationID: "a1"},
		{UserID: "u2", RoleKey: "engineers", AuthorizationID: "a2"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("ListUserGrants = %v, want %v", got, want)
	}
}

func TestListUserGrants_Pagination(t *testing.T) {
	f := newFakeServices()
	for i := 0; i < 230; i++ {
		f.auths["p1"] = append(f.auths["p1"], authWith("a"+strconv.Itoa(i), "u"+strconv.Itoa(i), "r"))
	}
	c := NewClientWithServices(f)

	got, err := c.ListUserGrants(context.Background(), "p1")
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if len(got) != 230 {
		t.Errorf("got %d grants, want 230", len(got))
	}
}

func TestAddUserGrant(t *testing.T) {
	f := newFakeServices()
	c := NewClientWithServices(f)
	if err := c.AddUserGrant(context.Background(), "p1", "u1", "engineers"); err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	want := []createAuthCall{{"p1", "u1", "engineers"}}
	if !reflect.DeepEqual(f.createAuthCalls, want) {
		t.Errorf("createAuthCalls = %v, want %v", f.createAuthCalls, want)
	}
}

func TestAddUserGrant_RejectsEmpty(t *testing.T) {
	c := NewClientWithServices(newFakeServices())
	cases := [][3]string{
		{"", "u", "r"},
		{"p", "", "r"},
		{"p", "u", ""},
	}
	for _, tc := range cases {
		if err := c.AddUserGrant(context.Background(), tc[0], tc[1], tc[2]); err == nil {
			t.Errorf("AddUserGrant(%q,%q,%q) returned nil, want error", tc[0], tc[1], tc[2])
		}
	}
}

func TestRemoveUserGrant_KeysOnAuthorizationID(t *testing.T) {
	f := newFakeServices()
	c := NewClientWithServices(f)
	if err := c.RemoveUserGrant(context.Background(), "p1", "u1", "engineers", "auth-xyz"); err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if !reflect.DeepEqual(f.deleteAuthCalls, []string{"auth-xyz"}) {
		t.Errorf("deleteAuthCalls = %v, want [auth-xyz]", f.deleteAuthCalls)
	}
}

func TestRemoveUserGrant_RequiresAuthorizationID(t *testing.T) {
	c := NewClientWithServices(newFakeServices())
	if err := c.RemoveUserGrant(context.Background(), "p", "u", "r", ""); err == nil {
		t.Fatal("expected error for empty authorizationID")
	}
}

func TestNewClient_RequiresArgs(t *testing.T) {
	if _, err := NewClient(context.Background(), "", "pat"); err == nil {
		t.Fatal("expected error for empty domain")
	}
	if _, err := NewClient(context.Background(), "example.zitadel.cloud", ""); err == nil {
		t.Fatal("expected error for empty pat")
	}
}

func TestClose_NoopOnFakeClient(t *testing.T) {
	c := NewClientWithServices(newFakeServices())
	if err := c.Close(); err != nil {
		t.Fatalf("Close on fake client returned error: %v", err)
	}
}

func TestClose_NoopOnNilClient(t *testing.T) {
	var c *Client
	if err := c.Close(); err != nil {
		t.Fatalf("Close on nil client returned error: %v", err)
	}
}

func TestAddProjectRole_ErrorPropagated(t *testing.T) {
	f := newFakeServices()
	f.addErr = errors.New("add failed")
	c := NewClientWithServices(f)
	if err := c.AddProjectRole(context.Background(), "p1", "k", "n", "g"); err == nil {
		t.Fatal("expected error from AddProjectRole")
	}
}

func TestRemoveProjectRole_ErrorPropagated(t *testing.T) {
	f := newFakeServices()
	f.removeErr = errors.New("remove failed")
	c := NewClientWithServices(f)
	if err := c.RemoveProjectRole(context.Background(), "p1", "k"); err == nil {
		t.Fatal("expected error from RemoveProjectRole")
	}
}

func TestAddUserGrant_ErrorPropagated(t *testing.T) {
	f := newFakeServices()
	f.createErr = errors.New("create failed")
	c := NewClientWithServices(f)
	if err := c.AddUserGrant(context.Background(), "p1", "u1", "r"); err == nil {
		t.Fatal("expected error from AddUserGrant")
	}
}

func TestRemoveUserGrant_ErrorPropagated(t *testing.T) {
	f := newFakeServices()
	f.deleteErr = errors.New("delete failed")
	c := NewClientWithServices(f)
	if err := c.RemoveUserGrant(context.Background(), "p1", "u1", "r", "auth-1"); err == nil {
		t.Fatal("expected error from RemoveUserGrant")
	}
}
