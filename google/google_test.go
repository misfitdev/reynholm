package google

import (
	"context"
	"errors"
	"reflect"
	"testing"

	admin "google.golang.org/api/admin/directory/v1"
)

// fakeDir is a hand-rolled Directory used by tests. Groups maps a normalized
// groupKey to a slice of pages of members; each page is a []*admin.Member.
type fakeDir struct {
	groups      map[string]*admin.Group
	pages       map[string][][]*admin.Member
	listErr     error
	getErr      error
	listCalls   int
	pageHistory []string
	// listGroupsPages maps "domain|query" to pages of groups.
	listGroupsPages map[string][][]*admin.Group
}

func newFakeDir() *fakeDir {
	return &fakeDir{
		groups: map[string]*admin.Group{},
		pages:  map[string][][]*admin.Member{},
	}
}

func (f *fakeDir) GetGroup(_ context.Context, groupKey string) (*admin.Group, error) {
	if f.getErr != nil {
		return nil, f.getErr
	}
	g, ok := f.groups[groupKey]
	if !ok {
		return nil, errors.New("not found")
	}
	return g, nil
}

func (f *fakeDir) ListMembersPage(_ context.Context, groupKey, pageToken string) ([]*admin.Member, string, error) {
	f.listCalls++
	f.pageHistory = append(f.pageHistory, groupKey+"|"+pageToken)
	if f.listErr != nil {
		return nil, "", f.listErr
	}
	pages := f.pages[groupKey]
	idx := 0
	if pageToken != "" {
		if pageToken[0] != 'p' {
			return nil, "", errors.New("bad token")
		}
		idx = int(pageToken[1] - '0')
	}
	if idx >= len(pages) {
		return nil, "", nil
	}
	next := ""
	if idx+1 < len(pages) {
		next = "p" + string(rune('0'+idx+1))
	}
	return pages[idx], next, nil
}

func (f *fakeDir) ListGroupsPage(_ context.Context, domain, query, pageToken string) ([]*admin.Group, string, error) {
	if f.listErr != nil {
		return nil, "", f.listErr
	}
	key := domain + "|" + query
	pages := f.listGroupsPages[key]
	idx := 0
	if pageToken != "" {
		if pageToken[0] != 'p' {
			return nil, "", errors.New("bad token")
		}
		idx = int(pageToken[1] - '0')
	}
	if idx >= len(pages) {
		return nil, "", nil
	}
	next := ""
	if idx+1 < len(pages) {
		next = "p" + string(rune('0'+idx+1))
	}
	return pages[idx], next, nil
}

func TestGetGroupDisplayName(t *testing.T) {
	fd := newFakeDir()
	fd.groups["engineers@example.com"] = &admin.Group{Name: "Engineering"}
	c := NewClientWithDirectory(fd)

	got, err := c.GetGroupDisplayName(context.Background(), "engineers@example.com")
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if got != "Engineering" {
		t.Errorf("name = %q, want %q", got, "Engineering")
	}
}

func TestGetGroupDisplayName_MissingKey(t *testing.T) {
	c := NewClientWithDirectory(newFakeDir())
	if _, err := c.GetGroupDisplayName(context.Background(), ""); err == nil {
		t.Fatal("expected error for empty groupKey")
	}
}

func TestListMembers_LowercaseAndFilterSuspended(t *testing.T) {
	fd := newFakeDir()
	fd.pages["engineers@example.com"] = [][]*admin.Member{{
		{Email: "Alice@Example.com", Type: "USER", Status: "ACTIVE"},
		{Email: "BOB@example.com", Type: "USER", Status: "SUSPENDED"},
		{Email: "carol@example.com", Type: "USER", Status: "ACTIVE"},
		{Email: "noStatus@example.com", Type: "USER"},
	}}
	c := NewClientWithDirectory(fd)

	got, err := c.ListMembers(context.Background(), "engineers@example.com")
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	want := []string{"alice@example.com", "carol@example.com"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("ListMembers = %v, want %v", got, want)
	}
}

func TestListMembers_Pagination(t *testing.T) {
	fd := newFakeDir()
	fd.pages["g@example.com"] = [][]*admin.Member{
		{
			{Email: "a@example.com", Type: "USER", Status: "ACTIVE"},
			{Email: "b@example.com", Type: "USER", Status: "ACTIVE"},
		},
		{
			{Email: "c@example.com", Type: "USER", Status: "ACTIVE"},
		},
		{
			{Email: "d@example.com", Type: "USER", Status: "ACTIVE"},
		},
	}
	c := NewClientWithDirectory(fd)

	got, err := c.ListMembers(context.Background(), "g@example.com")
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	want := []string{"a@example.com", "b@example.com", "c@example.com", "d@example.com"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("ListMembers = %v, want %v", got, want)
	}
	if fd.listCalls != 3 {
		t.Errorf("listCalls = %d, want 3", fd.listCalls)
	}
}

func TestListMembers_FlattenNestedGroups(t *testing.T) {
	fd := newFakeDir()
	fd.pages["parent@example.com"] = [][]*admin.Member{{
		{Email: "alice@example.com", Type: "USER", Status: "ACTIVE"},
		{Email: "nested@example.com", Type: "GROUP"},
	}}
	fd.pages["nested@example.com"] = [][]*admin.Member{{
		{Email: "Bob@example.com", Type: "USER", Status: "ACTIVE"},
		{Email: "DEEP@example.com", Type: "GROUP"},
		{Email: "suspended@example.com", Type: "USER", Status: "SUSPENDED"},
	}}
	fd.pages["DEEP@example.com"] = [][]*admin.Member{{
		{Email: "Carol@example.com", Type: "USER", Status: "ACTIVE"},
		{Email: "alice@example.com", Type: "USER", Status: "ACTIVE"}, // dup across groups
	}}
	c := NewClientWithDirectory(fd)

	got, err := c.ListMembers(context.Background(), "parent@example.com")
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	want := []string{"alice@example.com", "bob@example.com", "carol@example.com"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("ListMembers = %v, want %v", got, want)
	}
}

func TestListMembers_CycleSafe(t *testing.T) {
	fd := newFakeDir()
	fd.pages["a@example.com"] = [][]*admin.Member{{
		{Email: "u1@example.com", Type: "USER", Status: "ACTIVE"},
		{Email: "b@example.com", Type: "GROUP"},
	}}
	fd.pages["b@example.com"] = [][]*admin.Member{{
		{Email: "u2@example.com", Type: "USER", Status: "ACTIVE"},
		{Email: "A@example.com", Type: "GROUP"}, // cycle back via different casing
	}}
	c := NewClientWithDirectory(fd)

	got, err := c.ListMembers(context.Background(), "a@example.com")
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	want := []string{"u1@example.com", "u2@example.com"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("ListMembers = %v, want %v", got, want)
	}
}

func TestListMembers_EmptyEmailDropped(t *testing.T) {
	fd := newFakeDir()
	fd.pages["g@example.com"] = [][]*admin.Member{{
		{Email: "", Type: "USER", Status: "ACTIVE"},
		{Email: "ok@example.com", Type: "USER", Status: "ACTIVE"},
	}}
	c := NewClientWithDirectory(fd)

	got, err := c.ListMembers(context.Background(), "g@example.com")
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	want := []string{"ok@example.com"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("ListMembers = %v, want %v", got, want)
	}
}

func TestListMembers_PropagatesError(t *testing.T) {
	fd := newFakeDir()
	fd.listErr = errors.New("boom")
	c := NewClientWithDirectory(fd)

	_, err := c.ListMembers(context.Background(), "g@example.com")
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestListMembers_EmptyKey(t *testing.T) {
	c := NewClientWithDirectory(newFakeDir())
	if _, err := c.ListMembers(context.Background(), ""); err == nil {
		t.Fatal("expected error for empty groupKey")
	}
}

func TestNewClient_RequiresEnv(t *testing.T) {
	t.Setenv(envServiceAccount, "")
	t.Setenv(envAdminEmail, "")
	if _, err := NewClient(context.Background()); err == nil {
		t.Fatal("expected error when env vars missing")
	}

	t.Setenv(envServiceAccount, "sa@proj.iam.gserviceaccount.com")
	t.Setenv(envAdminEmail, "")
	if _, err := NewClient(context.Background()); err == nil {
		t.Fatal("expected error when admin email missing")
	}
}

func TestListGroups(t *testing.T) {
	fd := newFakeDir()
	fd.listGroupsPages = map[string][][]*admin.Group{
		"example.com|email:access-*": {{
			{Email: "access-eng@example.com"},
			{Email: "access-design@example.com"},
		}},
	}
	c := NewClientWithDirectory(fd)

	got, err := c.ListGroups(context.Background(), "example.com", "email:access-*")
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	want := []string{"access-design@example.com", "access-eng@example.com"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("ListGroups = %v, want %v", got, want)
	}
}

func TestListGroups_Pagination(t *testing.T) {
	fd := newFakeDir()
	fd.listGroupsPages = map[string][][]*admin.Group{
		"example.com|email:access-*": {
			{{Email: "access-a@example.com"}, {Email: "access-b@example.com"}},
			{{Email: "access-c@example.com"}},
		},
	}
	c := NewClientWithDirectory(fd)

	got, err := c.ListGroups(context.Background(), "example.com", "email:access-*")
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	want := []string{"access-a@example.com", "access-b@example.com", "access-c@example.com"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("ListGroups = %v, want %v", got, want)
	}
}

func TestListGroups_EmptyDomain(t *testing.T) {
	c := NewClientWithDirectory(newFakeDir())
	if _, err := c.ListGroups(context.Background(), "", "email:access-*"); err == nil {
		t.Fatal("expected error for empty domain")
	}
}
