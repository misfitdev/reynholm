// Package reconcile implements the core sync engine: it reads desired state
// from Google Workspace groups (as declared in the config), reads actual
// state from ZITADEL (roles and user grants on each managed project), and
// converges actual toward desired by creating missing roles, adding missing
// grants, and removing extraneous grants.
//
// The engine is scoped to RoleKeys explicitly listed in the config. Any
// ZITADEL roles or grants whose RoleKey is not in the config are left
// untouched, even if they carry the configured managed-group tag. This
// keeps the blast radius small while admins are onboarding groups.
//
// In dry-run mode, the engine logs the mutations it would perform without
// calling any mutating API.
package reconcile

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strings"

	"github.com/misfitdev/reynholm/config"
	"github.com/misfitdev/reynholm/zitadel"
)

// googleClient is the subset of the Google client used by Reconciler.
type googleClient interface {
	GetGroupDisplayName(ctx context.Context, groupKey string) (string, error)
	ListMembers(ctx context.Context, groupKey string) ([]string, error)
}

// zitadelClient is the subset of the ZITADEL client used by Reconciler.
type zitadelClient interface {
	LookupUserIDs(ctx context.Context) (map[string]string, error)
	ListProjectRoles(ctx context.Context, projectID, managedGroup string) (map[string]string, error)
	AddProjectRole(ctx context.Context, projectID, roleKey, displayName, managedGroup string) error
	ListUserGrants(ctx context.Context, projectID string) ([]zitadel.Grant, error)
	AddUserGrant(ctx context.Context, projectID, userID, roleKey string) error
	RemoveUserGrant(ctx context.Context, projectID, userID, roleKey, authorizationID string) error
}

// Reconciler converges ZITADEL state toward what the config declares should
// exist, based on live Google Workspace group membership.
type Reconciler struct {
	Google  googleClient
	Zitadel zitadelClient
	Config  *config.Config
	Logger  *slog.Logger
}

// New builds a Reconciler. Logger may be nil; the default slog logger is
// used in that case.
func New(g googleClient, z zitadelClient, cfg *config.Config, logger *slog.Logger) *Reconciler {
	if logger == nil {
		logger = slog.Default()
	}
	return &Reconciler{Google: g, Zitadel: z, Config: cfg, Logger: logger}
}

// Run executes one reconciliation pass over every project in the config.
// When dryRun is true, no mutating ZITADEL APIs are called; the planned
// actions are logged instead.
func (r *Reconciler) Run(ctx context.Context, dryRun bool) error {
	if r == nil {
		return errors.New("reconcile: nil receiver")
	}
	if r.Google == nil || r.Zitadel == nil {
		return errors.New("reconcile: google and zitadel clients are required")
	}
	if r.Config == nil {
		return errors.New("reconcile: config is required")
	}

	mode := "apply"
	if dryRun {
		mode = "dry-run"
	}
	r.Logger.Info("reconcile starting", "mode", mode, "projects", len(r.Config.Projects))

	r.Logger.Info("fetching zitadel users")
	emailToUserID, err := r.Zitadel.LookupUserIDs(ctx)
	if err != nil {
		return fmt.Errorf("reconcile: lookup zitadel users: %w", err)
	}
	r.Logger.Info("zitadel users fetched", "count", len(emailToUserID))

	for _, project := range r.Config.Projects {
		if err := r.reconcileProject(ctx, project, emailToUserID, dryRun); err != nil {
			return fmt.Errorf("reconcile: project %q: %w", project.ID, err)
		}
	}

	r.Logger.Info("reconcile finished", "mode", mode)
	return nil
}

// projectPlan captures the desired state for a single project: for each
// configured RoleKey, the display name and the set of ZITADEL UserIDs that
// should hold the grant.
type projectPlan struct {
	displayNames   map[string]string              // roleKey -> display name
	expectedByRole map[string]map[string]struct{} // roleKey -> set of userIDs
	roleKeys       []string                       // stable iteration order
}

func (r *Reconciler) reconcileProject(
	ctx context.Context,
	project config.Project,
	emailToUserID map[string]string,
	dryRun bool,
) error {
	logger := r.Logger.With("project_id", project.ID)
	logger.Info("project reconcile starting", "groups", len(project.Groups))

	plan, err := r.buildPlan(ctx, project, emailToUserID, logger)
	if err != nil {
		return err
	}

	if err := r.reconcileRoles(ctx, project.ID, plan, dryRun, logger); err != nil {
		return err
	}
	if err := r.reconcileGrants(ctx, project.ID, plan, dryRun, logger); err != nil {
		return err
	}

	logger.Info("project reconcile finished")
	return nil
}

// buildPlan walks every configured group, resolves Google membership, and
// maps members to ZITADEL UserIDs. Unmapped emails are logged and skipped.
func (r *Reconciler) buildPlan(
	ctx context.Context,
	project config.Project,
	emailToUserID map[string]string,
	logger *slog.Logger,
) (*projectPlan, error) {
	plan := &projectPlan{
		displayNames:   make(map[string]string, len(project.Groups)),
		expectedByRole: make(map[string]map[string]struct{}, len(project.Groups)),
		roleKeys:       make([]string, 0, len(project.Groups)),
	}

	seenRole := make(map[string]struct{}, len(project.Groups))
	for _, groupEmail := range project.Groups {
		roleKey, err := roleKeyFromEmail(groupEmail)
		if err != nil {
			return nil, fmt.Errorf("group %q: %w", groupEmail, err)
		}
		if _, dup := seenRole[roleKey]; dup {
			// Two groups producing the same RoleKey is a config error we
			// surface loudly rather than silently merge.
			return nil, fmt.Errorf("duplicate role key %q derived from group %q", roleKey, groupEmail)
		}
		seenRole[roleKey] = struct{}{}
		plan.roleKeys = append(plan.roleKeys, roleKey)

		gLogger := logger.With("group", groupEmail, "role_key", roleKey)

		displayName, err := r.Google.GetGroupDisplayName(ctx, groupEmail)
		if err != nil {
			return nil, fmt.Errorf("google get group %q: %w", groupEmail, err)
		}
		if displayName == "" {
			// Fall back to RoleKey so ZITADEL roles are not nameless.
			displayName = roleKey
		}
		plan.displayNames[roleKey] = displayName

		members, err := r.Google.ListMembers(ctx, groupEmail)
		if err != nil {
			return nil, fmt.Errorf("google list members %q: %w", groupEmail, err)
		}

		expected := make(map[string]struct{}, len(members))
		for _, email := range members {
			userID, ok := emailToUserID[email]
			if !ok {
				gLogger.Warn("skipping member without zitadel user", "email", email)
				continue
			}
			expected[userID] = struct{}{}
		}
		plan.expectedByRole[roleKey] = expected
		gLogger.Info("group resolved", "display_name", displayName, "members", len(members), "mapped", len(expected))
	}
	return plan, nil
}

// reconcileRoles creates any configured RoleKey that is not yet present in
// ZITADEL under the managed group. Roles outside the config are not touched.
func (r *Reconciler) reconcileRoles(
	ctx context.Context,
	projectID string,
	plan *projectPlan,
	dryRun bool,
	logger *slog.Logger,
) error {
	actualRoles, err := r.Zitadel.ListProjectRoles(ctx, projectID, r.Config.ManagedGroup)
	if err != nil {
		return fmt.Errorf("zitadel list roles: %w", err)
	}
	for _, roleKey := range plan.roleKeys {
		if _, present := actualRoles[roleKey]; present {
			continue
		}
		displayName := plan.displayNames[roleKey]
		if dryRun {
			logger.Info("would create role", "role_key", roleKey, "display_name", displayName)
			continue
		}
		logger.Info("creating role", "role_key", roleKey, "display_name", displayName)
		if err := r.Zitadel.AddProjectRole(ctx, projectID, roleKey, displayName, r.Config.ManagedGroup); err != nil {
			return fmt.Errorf("zitadel add project role %q: %w", roleKey, err)
		}
	}
	return nil
}

// reconcileGrants diffs expected user grants (from Google) against actual
// grants (from ZITADEL), restricted to the configured RoleKeys. It then
// adds missing grants and removes extraneous ones.
func (r *Reconciler) reconcileGrants(
	ctx context.Context,
	projectID string,
	plan *projectPlan,
	dryRun bool,
	logger *slog.Logger,
) error {
	allGrants, err := r.Zitadel.ListUserGrants(ctx, projectID)
	if err != nil {
		return fmt.Errorf("zitadel list grants: %w", err)
	}

	// Bucket actual grants by RoleKey, but only for RoleKeys we manage.
	managed := make(map[string]struct{}, len(plan.roleKeys))
	for _, k := range plan.roleKeys {
		managed[k] = struct{}{}
	}
	actualByRole := make(map[string][]zitadel.Grant, len(plan.roleKeys))
	for _, g := range allGrants {
		if _, ok := managed[g.RoleKey]; !ok {
			continue
		}
		actualByRole[g.RoleKey] = append(actualByRole[g.RoleKey], g)
	}

	for _, roleKey := range plan.roleKeys {
		expected := plan.expectedByRole[roleKey]
		actual := actualByRole[roleKey]

		// Index actual grants by UserID for diffing. A user may appear in
		// multiple authorizations for the same role (rare but possible);
		// we keep every AuthorizationID so we can fully clean up dupes.
		actualByUser := make(map[string][]zitadel.Grant, len(actual))
		for _, g := range actual {
			actualByUser[g.UserID] = append(actualByUser[g.UserID], g)
		}

		// Missing: expected - actual.
		missing := make([]string, 0)
		for userID := range expected {
			if _, present := actualByUser[userID]; !present {
				missing = append(missing, userID)
			}
		}
		sort.Strings(missing)

		// Extraneous: actual - expected. If a user is duplicated under the
		// same role, every duplicate is removed; if the user is expected,
		// the first grant is kept and the rest are removed.
		extraneous := make([]zitadel.Grant, 0)
		for userID, grants := range actualByUser {
			if _, want := expected[userID]; !want {
				extraneous = append(extraneous, grants...)
				continue
			}
			if len(grants) > 1 {
				extraneous = append(extraneous, grants[1:]...)
			}
		}
		sort.Slice(extraneous, func(i, j int) bool {
			if extraneous[i].UserID != extraneous[j].UserID {
				return extraneous[i].UserID < extraneous[j].UserID
			}
			return extraneous[i].AuthorizationID < extraneous[j].AuthorizationID
		})

		rLogger := logger.With("role_key", roleKey)
		rLogger.Info("grants diff",
			"expected", len(expected),
			"actual_users", len(actualByUser),
			"missing", len(missing),
			"extraneous", len(extraneous),
		)

		for _, userID := range missing {
			if dryRun {
				rLogger.Info("would add grant", "user_id", userID)
				continue
			}
			rLogger.Info("adding grant", "user_id", userID)
			if err := r.Zitadel.AddUserGrant(ctx, projectID, userID, roleKey); err != nil {
				return fmt.Errorf("zitadel add grant user=%q role=%q: %w", userID, roleKey, err)
			}
		}
		for _, g := range extraneous {
			if dryRun {
				rLogger.Info("would remove grant",
					"user_id", g.UserID, "authorization_id", g.AuthorizationID)
				continue
			}
			rLogger.Info("removing grant",
				"user_id", g.UserID, "authorization_id", g.AuthorizationID)
			if err := r.Zitadel.RemoveUserGrant(ctx, projectID, g.UserID, g.RoleKey, g.AuthorizationID); err != nil {
				return fmt.Errorf("zitadel remove grant auth=%q: %w", g.AuthorizationID, err)
			}
		}
	}
	return nil
}

// roleKeyFromEmail returns the local part of an email address (everything
// before "@"), which we use as the ZITADEL RoleKey. Empty local parts or
// missing "@" are reported as errors so config typos surface immediately.
func roleKeyFromEmail(email string) (string, error) {
	at := strings.IndexByte(email, '@')
	if at <= 0 {
		return "", fmt.Errorf("invalid group email %q: missing local part", email)
	}
	return email[:at], nil
}
