---
paths: ".github/workflows/*.yml"
---

# GitHub Actions Pinning

Pin every `uses:` action to a full commit SHA, with the human-readable version as a trailing comment:

```yaml
- uses: actions/checkout@3d3c42e5aac5ba805825da76410c181273ba90b1 # v7.0.1
```

This holds for **all** actions — GitHub-authored and third-party alike — so the supply chain is uniform and auditable: a tag can be repointed, a commit SHA cannot. The `github-actions` Dependabot ecosystem bumps the SHAs and updates the version comment, so pinning does not freeze versions.

When adding or updating an action, resolve the tag to its SHA rather than using the bare tag:

```sh
gh api repos/<owner>/<repo>/commits/<tag> --jq '.sha'
```
