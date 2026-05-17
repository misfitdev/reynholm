package reconcile

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"reflect"
	"sort"
	"testing"

	"github.com/misfitdev/reynholm/config"
	"github.com/misfitdev/reynholm/zitadel"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type fakeGoogle struct {
	displayNames map[string]string
	members      map[string][]string
	displayErr   error
	membersErr   error
	// listGroups maps "domain|query" to group emails.
	listGroups    map[string][]string
	listGroupsErr error
}

func (f *fakeGoogle) GetGroupDisplayName(_ context.Context, groupKey string) (string, error) {
	if f.displayErr != nil {
		return "", f.displayErr
	}
	return f.displayNames[groupKey], nil
}

func (f *fakeGoogle) ListMembers(_ context.Context, groupKey string) ([]string, error) {
	if f.membersErr != nil {
		return nil, f.membersErr
	}
	return f.members[groupKey], nil
}

func (f *fakeGoogle) ListGroups(_ context.Context, domain, query string) ([]string, error) {
	if f.listGroupsErr != nil {
		return nil, f.listGroupsErr
	}
	return f.listGroups[domain+"|"+query], nil
}

type addRoleCall struct {
	ProjectID, RoleKey, DisplayName, Group string
}
type removeRoleCall struct {
	ProjectID, RoleKey string
}
type addGrantCall struct {
	ProjectID, UserID, RoleKey string
}
type removeGrantCall struct {
	ProjectID, UserID, RoleKey, AuthorizationID string
}

type fakeZitadel struct {
	users           map[string]string
	rolesByProject  map[string]map[string]string
	grantsByProject map[string][]zitadel.Grant

	addRoleCalls     []addRoleCall
	removeRoleCalls  []removeRoleCall
	addGrantCalls    []addGrantCall
	removeGrantCalls []removeGrantCall

	lookupErr      error
	rolesErr       error
	grantsErr      error
	addRoleErr     error
	removeRoleErr  error
	addGrantErr    error
	removeGrantErr error
}

func newFakeZitadel() *fakeZitadel {
	return &fakeZitadel{
		users:           map[string]string{},
		rolesByProject:  map[string]map[string]string{},
		grantsByProject: map[string][]zitadel.Grant{},
	}
}

func (f *fakeZitadel) LookupUserIDs(_ context.Context) (map[string]string, error) {
	if f.lookupErr != nil {
		return nil, f.lookupErr
	}
	return f.users, nil
}

func (f *fakeZitadel) ListProjectRoles(_ context.Context, projectID, _ string) (map[string]string, error) {
	if f.rolesErr != nil {
		return nil, f.rolesErr
	}
	out := map[string]string{}
	for k, v := range f.rolesByProject[projectID] {
		out[k] = v
	}
	return out, nil
}

func (f *fakeZitadel) AddProjectRole(_ context.Context, projectID, roleKey, displayName, group string) error {
	if f.addRoleErr != nil {
		return f.addRoleErr
	}
	f.addRoleCalls = append(f.addRoleCalls, addRoleCall{projectID, roleKey, displayName, group})
	if f.rolesByProject[projectID] == nil {
		f.rolesByProject[projectID] = map[string]string{}
	}
	f.rolesByProject[projectID][roleKey] = displayName
	return nil
}

func (f *fakeZitadel) RemoveProjectRole(_ context.Context, projectID, roleKey string) error {
	if f.removeRoleErr != nil {
		return f.removeRoleErr
	}
	f.removeRoleCalls = append(f.removeRoleCalls, removeRoleCall{projectID, roleKey})
	delete(f.rolesByProject[projectID], roleKey)
	return nil
}

func (f *fakeZitadel) ListUserGrants(_ context.Context, projectID string) ([]zitadel.Grant, error) {
	if f.grantsErr != nil {
		return nil, f.grantsErr
	}
	return append([]zitadel.Grant(nil), f.grantsByProject[projectID]...), nil
}

func (f *fakeZitadel) AddUserGrant(_ context.Context, projectID, userID, roleKey string) error {
	if f.addGrantErr != nil {
		return f.addGrantErr
	}
	f.addGrantCalls = append(f.addGrantCalls, addGrantCall{projectID, userID, roleKey})
	return nil
}

func (f *fakeZitadel) RemoveUserGrant(_ context.Context, projectID, userID, roleKey, authorizationID string) error {
	if f.removeGrantErr != nil {
		return f.removeGrantErr
	}
	f.removeGrantCalls = append(f.removeGrantCalls, removeGrantCall{projectID, userID, roleKey, authorizationID})
	return nil
}

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, &slog.HandlerOptions{Level: slog.LevelDebug}))
}

func baseCfg() *config.Config {
	return &config.Config{
		GoogleDomain: "example.com",
		ManagedGroup: "google-sync",
		Projects: []config.Project{
			{
				ID:     "p1",
				Groups: []string{"engineers@example.com", "sre@example.com"},
			},
		},
	}
}

// fixtureFakes builds Google + ZITADEL fakes with a realistic, mixed state:
// - one configured role already exists in ZITADEL, one is missing
// - one expected user is already granted, one is missing, one is extraneous
// - one unmapped email (should be skipped with a warning)
// - one role+grant outside the managed config (should be untouched)
func fixtureFakes() (*fakeGoogle, *fakeZitadel) {
	g := &fakeGoogle{
		displayNames: map[string]string{
			"engineers@example.com": "Engineering",
			"sre@example.com":       "SRE",
		},
		members: map[string][]string{
			"engineers@example.com": {
				"alice@example.com",
				"bob@example.com",
				"ghost@example.com", // not in ZITADEL: must be skipped
			},
			"sre@example.com": {
				"carol@example.com",
			},
		},
	}
	z := newFakeZitadel()
	z.users = map[string]string{
		"alice@example.com": "u-alice",
		"bob@example.com":   "u-bob",
		"carol@example.com": "u-carol",
		"dave@example.com":  "u-dave",
	}
	z.rolesByProject["p1"] = map[string]string{
		"engineers": "Engineering",
		// "sre" is missing - reconciler must create it.
	}
	z.grantsByProject["p1"] = []zitadel.Grant{
		{UserID: "u-alice", RoleKey: "engineers", AuthorizationID: "a-alice"},
		// u-bob is expected on engineers but missing.
		{UserID: "u-dave", RoleKey: "engineers", AuthorizationID: "a-dave"}, // extraneous
		// u-carol is expected on sre but missing.
		{UserID: "u-eve", RoleKey: "legacy", AuthorizationID: "a-eve"}, // unmanaged role: ignore
	}
	return g, z
}

func TestRun_DryRun_MakesNoMutations(t *testing.T) {
	g, z := fixtureFakes()
	r := New(g, z, baseCfg(), discardLogger())

	if err := r.Run(context.Background(), true); err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if len(z.addRoleCalls) != 0 {
		t.Errorf("addRoleCalls = %v, want none in dry-run", z.addRoleCalls)
	}
	if len(z.addGrantCalls) != 0 {
		t.Errorf("addGrantCalls = %v, want none in dry-run", z.addGrantCalls)
	}
	if len(z.removeGrantCalls) != 0 {
		t.Errorf("removeGrantCalls = %v, want none in dry-run", z.removeGrantCalls)
	}
	if len(z.removeRoleCalls) != 0 {
		t.Errorf("removeRoleCalls = %v, want none in dry-run", z.removeRoleCalls)
	}
}

func TestRun_Apply_CreatesRoleAddsAndRemovesGrants(t *testing.T) {
	g, z := fixtureFakes()
	r := New(g, z, baseCfg(), discardLogger())

	if err := r.Run(context.Background(), false); err != nil {
		t.Fatalf("unexpected err: %v", err)
	}

	wantRoles := []addRoleCall{{"p1", "sre", "SRE", "google-sync"}}
	if !reflect.DeepEqual(z.addRoleCalls, wantRoles) {
		t.Errorf("addRoleCalls = %v, want %v", z.addRoleCalls, wantRoles)
	}

	gotAdds := append([]addGrantCall(nil), z.addGrantCalls...)
	sort.Slice(gotAdds, func(i, j int) bool {
		if gotAdds[i].RoleKey != gotAdds[j].RoleKey {
			return gotAdds[i].RoleKey < gotAdds[j].RoleKey
		}
		return gotAdds[i].UserID < gotAdds[j].UserID
	})
	wantAdds := []addGrantCall{
		{"p1", "u-bob", "engineers"},
		{"p1", "u-carol", "sre"},
	}
	if !reflect.DeepEqual(gotAdds, wantAdds) {
		t.Errorf("addGrantCalls = %v, want %v", gotAdds, wantAdds)
	}

	wantRemoves := []removeGrantCall{{"p1", "u-dave", "engineers", "a-dave"}}
	if !reflect.DeepEqual(z.removeGrantCalls, wantRemoves) {
		t.Errorf("removeGrantCalls = %v, want %v", z.removeGrantCalls, wantRemoves)
	}
}

func TestRun_IgnoresUnmanagedRoles(t *testing.T) {
	g, z := fixtureFakes()
	// Add an unmanaged grant that uses a RoleKey not in the config.
	z.grantsByProject["p1"] = append(z.grantsByProject["p1"], zitadel.Grant{
		UserID: "u-alice", RoleKey: "external-role", AuthorizationID: "a-external",
	})
	r := New(g, z, baseCfg(), discardLogger())

	if err := r.Run(context.Background(), false); err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	for _, c := range z.removeGrantCalls {
		if c.RoleKey == "external-role" || c.AuthorizationID == "a-external" {
			t.Errorf("reconciler touched unmanaged role: %+v", c)
		}
	}
}

func TestRun_SkipsUnmappedEmails(t *testing.T) {
	g, z := fixtureFakes()
	// ghost@example.com is in Google but not in ZITADEL.
	r := New(g, z, baseCfg(), discardLogger())

	if err := r.Run(context.Background(), false); err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	for _, c := range z.addGrantCalls {
		if c.UserID == "" {
			t.Errorf("unexpected empty UserID in addGrantCalls: %+v", c)
		}
	}
}

func TestRun_RoleKeyDerivation(t *testing.T) {
	cases := []struct {
		email   string
		want    string
		wantErr bool
	}{
		{"engineers@example.com", "engineers", false},
		{"a@b", "a", false},
		{"@example.com", "", true},
		{"noatsign", "", true},
		{"", "", true},
	}
	for _, tc := range cases {
		got, err := roleKeyFromEmail(tc.email)
		if (err != nil) != tc.wantErr {
			t.Errorf("roleKeyFromEmail(%q): err=%v, wantErr=%v", tc.email, err, tc.wantErr)
		}
		if got != tc.want {
			t.Errorf("roleKeyFromEmail(%q): got=%q, want=%q", tc.email, got, tc.want)
		}
	}
}

func TestRun_DuplicateRoleKeyAcrossGroups(t *testing.T) {
	cfg := baseCfg()
	cfg.Projects[0].Groups = []string{"engineers@example.com", "engineers@other.com"}
	g, z := fixtureFakes()
	g.displayNames["engineers@other.com"] = "Engineering Alt"
	g.members["engineers@other.com"] = []string{"alice@example.com"}

	r := New(g, z, cfg, discardLogger())
	if err := r.Run(context.Background(), true); err == nil {
		t.Fatal("expected error for duplicate role key")
	}
}

func TestRun_PropagatesLookupErr(t *testing.T) {
	g, z := fixtureFakes()
	z.lookupErr = errors.New("boom")
	r := New(g, z, baseCfg(), discardLogger())
	if err := r.Run(context.Background(), true); err == nil {
		t.Fatal("expected error from LookupUserIDs")
	}
}

func TestRun_PropagatesGoogleErr(t *testing.T) {
	g, z := fixtureFakes()
	g.membersErr = errors.New("google down")
	r := New(g, z, baseCfg(), discardLogger())
	if err := r.Run(context.Background(), true); err == nil {
		t.Fatal("expected error from Google list members")
	}
}

func TestRun_PropagatesListRolesErr(t *testing.T) {
	g, z := fixtureFakes()
	z.rolesErr = errors.New("zitadel down")
	r := New(g, z, baseCfg(), discardLogger())
	if err := r.Run(context.Background(), false); err == nil {
		t.Fatal("expected error from ListProjectRoles")
	}
}

func TestRun_PropagatesAddRoleErr(t *testing.T) {
	g, z := fixtureFakes()
	z.addRoleErr = errors.New("add role failed")
	r := New(g, z, baseCfg(), discardLogger())
	if err := r.Run(context.Background(), false); err == nil {
		t.Fatal("expected error from AddProjectRole")
	}
}

func TestRun_PropagatesAddGrantErr(t *testing.T) {
	g, z := fixtureFakes()
	z.addGrantErr = errors.New("add grant failed")
	// Pre-create the missing role so we get past the role step.
	z.rolesByProject["p1"]["sre"] = "SRE"
	r := New(g, z, baseCfg(), discardLogger())
	if err := r.Run(context.Background(), false); err == nil {
		t.Fatal("expected error from AddUserGrant")
	}
}

func TestRun_PropagatesRemoveGrantErr(t *testing.T) {
	g, z := fixtureFakes()
	z.removeGrantErr = errors.New("remove grant failed")
	z.rolesByProject["p1"]["sre"] = "SRE"
	r := New(g, z, baseCfg(), discardLogger())
	if err := r.Run(context.Background(), false); err == nil {
		t.Fatal("expected error from RemoveUserGrant")
	}
}

func TestRun_FallbackDisplayName(t *testing.T) {
	g, z := fixtureFakes()
	g.displayNames["sre@example.com"] = "" // empty -> should fall back to RoleKey
	r := New(g, z, baseCfg(), discardLogger())
	if err := r.Run(context.Background(), false); err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	for _, c := range z.addRoleCalls {
		if c.RoleKey == "sre" && c.DisplayName != "sre" {
			t.Errorf("expected fallback display name %q, got %q", "sre", c.DisplayName)
		}
	}
}

func TestRun_NilClientsFail(t *testing.T) {
	r := &Reconciler{Logger: discardLogger(), Config: baseCfg()}
	if err := r.Run(context.Background(), true); err == nil {
		t.Fatal("expected error for nil clients")
	}
}

func TestRun_NilConfigFails(t *testing.T) {
	r := &Reconciler{Google: &fakeGoogle{}, Zitadel: newFakeZitadel(), Logger: discardLogger()}
	if err := r.Run(context.Background(), true); err == nil {
		t.Fatal("expected error for nil config")
	}
}

func TestRun_RemovesStaleRoles(t *testing.T) {
	g, z := fixtureFakes()
	// "legacy-team" exists in ZITADEL under the managed group but is not in the config.
	z.rolesByProject["p1"]["legacy-team"] = "Legacy Team"
	r := New(g, z, baseCfg(), discardLogger())

	if err := r.Run(context.Background(), false); err != nil {
		t.Fatalf("unexpected err: %v", err)
	}

	wantRemoves := []removeRoleCall{{"p1", "legacy-team"}}
	if !reflect.DeepEqual(z.removeRoleCalls, wantRemoves) {
		t.Errorf("removeRoleCalls = %v, want %v", z.removeRoleCalls, wantRemoves)
	}
}

func TestRun_DryRun_DoesNotRemoveStaleRoles(t *testing.T) {
	g, z := fixtureFakes()
	z.rolesByProject["p1"]["legacy-team"] = "Legacy Team"
	r := New(g, z, baseCfg(), discardLogger())

	if err := r.Run(context.Background(), true); err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if len(z.removeRoleCalls) != 0 {
		t.Errorf("removeRoleCalls = %v, want none in dry-run", z.removeRoleCalls)
	}
}

func TestRun_PropagatesRemoveRoleErr(t *testing.T) {
	g, z := fixtureFakes()
	z.rolesByProject["p1"]["legacy-team"] = "Legacy Team"
	z.removeRoleErr = errors.New("remove role failed")
	r := New(g, z, baseCfg(), discardLogger())
	if err := r.Run(context.Background(), false); err == nil {
		t.Fatal("expected error from RemoveProjectRole")
	}
}

func TestRun_DuplicateGrantSameRoleIsCleanedUp(t *testing.T) {
	g, z := fixtureFakes()
	// alice gets two grants for the same role - the extra one must be removed.
	z.grantsByProject["p1"] = append(z.grantsByProject["p1"], zitadel.Grant{
		UserID: "u-alice", RoleKey: "engineers", AuthorizationID: "a-alice-dup",
	})
	r := New(g, z, baseCfg(), discardLogger())
	if err := r.Run(context.Background(), false); err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	found := false
	for _, c := range z.removeGrantCalls {
		if c.UserID == "u-alice" && c.AuthorizationID == "a-alice-dup" {
			found = true
		}
	}
	if !found {
		t.Errorf("expected duplicate grant for alice to be removed, got removes=%v", z.removeGrantCalls)
	}
}

func TestRun_GlobExpansion(t *testing.T) {
	g := &fakeGoogle{
		displayNames: map[string]string{
			"access-eng@example.com":    "Engineering Access",
			"access-design@example.com": "Design Access",
		},
		members: map[string][]string{
			"access-eng@example.com":    {"alice@example.com"},
			"access-design@example.com": {"bob@example.com"},
		},
		listGroups: map[string][]string{
			"example.com|email:access-*": {
				"access-design@example.com",
				"access-eng@example.com",
			},
		},
	}
	z := newFakeZitadel()
	z.users = map[string]string{
		"alice@example.com": "u-alice",
		"bob@example.com":   "u-bob",
	}
	z.rolesByProject["p1"] = map[string]string{}
	cfg := &config.Config{
		GoogleDomain: "example.com",
		ManagedGroup: "google-sync",
		Projects: []config.Project{
			{ID: "p1", Groups: []string{"access-*@example.com"}},
		},
	}
	r := New(g, z, cfg, discardLogger())

	if err := r.Run(context.Background(), false); err != nil {
		t.Fatalf("unexpected err: %v", err)
	}

	wantRoles := []addRoleCall{
		{"p1", "access-design", "Design Access", "google-sync"},
		{"p1", "access-eng", "Engineering Access", "google-sync"},
	}
	if !reflect.DeepEqual(z.addRoleCalls, wantRoles) {
		t.Errorf("addRoleCalls = %v, want %v", z.addRoleCalls, wantRoles)
	}

	gotAdds := append([]addGrantCall(nil), z.addGrantCalls...)
	sort.Slice(gotAdds, func(i, j int) bool { return gotAdds[i].UserID < gotAdds[j].UserID })
	wantAdds := []addGrantCall{
		{"p1", "u-alice", "access-eng"},
		{"p1", "u-bob", "access-design"},
	}
	if !reflect.DeepEqual(gotAdds, wantAdds) {
		t.Errorf("addGrantCalls = %v, want %v", gotAdds, wantAdds)
	}
}

func TestRun_GlobExpansionError(t *testing.T) {
	g := &fakeGoogle{
		listGroupsErr: errors.New("api down"),
	}
	z := newFakeZitadel()
	z.users = map[string]string{"a@example.com": "u-a"}
	cfg := &config.Config{
		GoogleDomain: "example.com",
		ManagedGroup: "google-sync",
		Projects: []config.Project{
			{ID: "p1", Groups: []string{"access-*@example.com"}},
		},
	}
	r := New(g, z, cfg, discardLogger())
	if err := r.Run(context.Background(), true); err == nil {
		t.Fatal("expected error from glob expansion")
	}
}

func TestRun_SkipsAlreadyExistsOnAddGrant(t *testing.T) {
	g, z := fixtureFakes()
	z.addGrantErr = status.Error(codes.AlreadyExists, "grant already exists")
	z.rolesByProject["p1"]["sre"] = "SRE"
	r := New(g, z, baseCfg(), discardLogger())

	if err := r.Run(context.Background(), false); err != nil {
		t.Fatalf("expected AlreadyExists to be skipped, got: %v", err)
	}
}
