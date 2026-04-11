# Versioning

kubeswarm follows [Semantic Versioning 2.0.0](https://semver.org/).

## Current Phase

**Pre-1.0 (alpha).** CRD schemas and APIs may change between minor versions without
deprecation notices. This is expected and will stabilize before 1.0.

## Tag Format

```
v{major}.{minor}.{patch}[-{prerelease}]
```

| Tag | Meaning |
|-----|---------|
| `v0.1.0-alpha.1` | Early development. Breaking changes expected. |
| `v0.1.0-beta.1` | Feature-complete for this release. Stabilizing. |
| `v0.1.0-rc.1` | Release candidate. Bugfixes only. |
| `v0.1.0` | Stable release. |

## When to Bump

| Change | Version bump |
|--------|-------------|
| Bugfixes, dependency updates, docs | Patch (`0.1.0` -> `0.5.1`) |
| New features, new CRDs, RFC implementations | Minor (`0.1.0` -> `0.6.0`) |
| Breaking CRD schema changes (post-1.0 only) | Major (`1.0.0` -> `2.0.0`) |

**Pre-1.0:** Minor version bumps may include breaking changes.

## Release Process

1. Ensure CI is green on `main`.
2. Tag: `git tag v0.X.0-alpha.N`
3. Push: `git push origin v0.X.0-alpha.N`
4. The release workflow automatically:
   - Builds and scans container images (controller + runtime)
   - Generates SBOMs (SPDX format)
   - Pushes images to `ghcr.io/kubeswarm/`
   - Creates a GitHub Release with `install.yaml` and SBOMs

Stable tags (no `-` suffix) also trigger:
- Helm chart sync (`helm-charts` repo)
- Documentation sync (`kubeswarm-docs` repo)

Prerelease tags do **not** auto-sync downstream repos.

## Cross-Repo Versioning

| Repo | Versioning |
|------|-----------|
| `kubeswarm` (controller) | Version anchor for the project |
| `helm-charts` | Tracks operator version via `appVersion` in Chart.yaml |
| `kubeswarm-cli` | Independent versioning |
| `kubeswarm-docs` | Independent versioning |
| `kubeswarm-rfcs` | No versioning (design documents) |
| `kubeswarm-bench` | Independent versioning |
| `kubeswarm-cookbook` | No versioning (examples) |

## API Stability

| API Group | Current | Stability |
|-----------|---------|-----------|
| `kubeswarm.io/v1alpha1` | Active | Breaking changes possible between minor versions |
| `kubeswarm.io/v1beta1` | Planned | Breaking changes only with deprecation notice |
| `kubeswarm.io/v1` | Future | No breaking changes without major version bump |

The transition from `v1alpha1` to `v1beta1` will include a conversion webhook for
zero-downtime migration. This is planned for after the CRD surface stabilizes.
