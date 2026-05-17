<h1 align="center">reynholm</h1>

<p align="center">
<a href="https://github.com/misfitdev/reynholm/actions/workflows/ci.yml"><img src="https://github.com/misfitdev/reynholm/actions/workflows/ci.yml/badge.svg" alt="CI"></a>
<a href="https://github.com/misfitdev/reynholm/actions/workflows/release.yml"><img src="https://github.com/misfitdev/reynholm/actions/workflows/release.yml/badge.svg" alt="Release"></a>
<a href="LICENSE"><img src="https://img.shields.io/badge/license-MIT-blue.svg" alt="License: MIT"></a>
</p>

<p align="center">
Sync Google Workspace group memberships into ZITADEL project role grants.
Runs statelessly: reads current state from both systems, diffs, and corrects.
</p>

<p align="center">
<a href="#install">Install</a> &middot;
<a href="#quick-start">Quick start</a> &middot;
<a href="#flags">Flags</a> &middot;
<a href="#config">Config</a> &middot;
<a href="#how-it-works">How it works</a> &middot;
<a href="#security">Security</a>
</p>

---

## Install

<details>
<summary>From source</summary>

> ```sh
> go install github.com/misfitdev/reynholm@latest
> ```
>
> Or clone and build:
>
> ```sh
> git clone https://github.com/misfitdev/reynholm.git
> cd reynholm
> just build
> ```

</details>

<details>
<summary>Container</summary>

> ```sh
> docker build -t reynholm .
> docker run --rm -v $(pwd)/config.yaml:/config.yaml reynholm --config /config.yaml
> ```

</details>

## Quick start

```sh
# 1. Create a config (see config.example.yaml)
cp config.example.yaml reynholm.yaml

# 2. Set credentials
export GOOGLE_APPLICATION_CREDENTIALS=/path/to/service-account.json
export GOOGLE_SERVICE_ACCOUNT_EMAIL=sync@project.iam.gserviceaccount.com
export GOOGLE_ADMIN_EMAIL=admin@example.com
export ZITADEL_DOMAIN=https://example.zitadel.cloud
export ZITADEL_PAT=your-service-user-pat

# 3. Preview what would change (default: dry-run)
reynholm --config reynholm.yaml

# 4. Apply changes
reynholm --config reynholm.yaml --apply
```

## Flags

| Flag | Default | Description |
|------|---------|-------------|
| `--config` | (required) | Path to YAML config file |
| `--apply` | `false` | Commit changes to ZITADEL |
| `--dry-run` | `false` | Preview only; overrides `--apply` when both are set |
| `--log-level` | `info` | `debug` / `info` / `warn` / `error` |
| `--log-format` | `text` | `text` or `json` |
| `--version` | | Print version and exit |

## Config

Config is a single YAML file mapping Google Workspace groups to ZITADEL projects:

```yaml
google_domain: example.com
managed_group: reynholm

projects:
  - id: "123456789012345678"
    groups:
      - engineering@example.com
      - design@example.com
      - access-*@example.com   # wildcard: expands to all matching groups

  - id: "987654321098765432"
    groups:
      - employees@example.com
```

| Field | Description |
|-------|-------------|
| `google_domain` | Your Google Workspace primary domain |
| `managed_group` | ZITADEL role group label marking reynholm ownership |
| `projects` | List of ZITADEL projects to sync |
| `projects[].id` | ZITADEL project ID |
| `projects[].groups` | Google Workspace group emails (or `*` wildcard patterns) whose members get this project's roles |

### Wildcard groups

Entries containing `*` are expanded at runtime via the Directory API. The `*` acts as a prefix wildcard on the local part (everything before `@`):

```yaml
groups:
  - access-*@example.com   # matches access-eng@, access-design@, etc.
```

The domain is used to scope the API query; the local-part prefix (e.g. `access-`) filters by email. Each matched group is processed as if it were listed individually.

### Role mapping

For each group listed under a project, reynholm ensures a role exists on that ZITADEL project:

- **Role key** = email local part (`engineering@example.com` → `engineering`)
- **Display name** = group name from Google
- **Group label** = the configured `managed_group` value

A Google group can map to multiple projects. Users in that group get grants on every project that lists it.

## How it works

reynholm is a stateless reconciler. Every run:

1. **Reads config** -- YAML mapping of projects to Google groups.
2. **Fetches from Google** -- group display names and flattened member lists (recursively resolves nested groups, filters suspended users).
3. **Fetches from ZITADEL** -- existing project roles and user grants.
4. **Translates emails to UserIDs** -- maps Google member emails to ZITADEL user IDs. Warns and skips users not yet synced to ZITADEL.
5. **Diffs and corrects**:
   - Creates missing project roles (tagged with `managed_group`).
   - Removes stale roles that carry the `managed_group` tag but are no longer in the config.
   - Adds missing user grants.
   - Removes extraneous grants for users no longer in the corresponding Google group.

reynholm **only** touches roles and grants tagged with its `managed_group` label. Manually created roles and grants are never modified.

## Security

- ZITADEL authentication uses a service user Personal Access Token (PAT).
- Google authentication uses a service account with domain-wide delegation, impersonating a Workspace admin.
- The PAT and service account key should be treated as secrets and injected via environment variables or a secrets manager.

See [SECURITY.md](SECURITY.md) for the vulnerability reporting policy.

## License

MIT
