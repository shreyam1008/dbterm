# Website and APT hosting

Cloudflare Pages project `dbterm` publishes https://dbterm.shreyam1008.com.np/.
The GitHub production branch is `gh-pages`, output directory `.`, with no build
command and `SKIP_DEPENDENCY_INSTALL=true`. Deploy every change on that branch.

Keep the existing release and Website workflows: the release publishes signed
APT metadata/packages, then Website builds the matching static site and updates
`gh-pages` while preserving `apt/`. Cloudflare publishes the complete branch.
Pointing Pages at `main:site` would bypass that ordering and omit the signed
repository. Signing keys remain in GitHub Actions, not Cloudflare.

`site/public/_headers` makes APT metadata revalidate rather than use a stale
browser copy. Retain the explicit static 404, canonical metadata, package hashes
and public signing keys.

The existing `dbterm-discovery` Worker route adds discovery headers and serves
`product.md` for homepage requests accepting Markdown. Its origin fetch must
continue to work with Pages; check both normal HTML and Markdown after DNS changes.

GitHub Pages hosting was disabled on 9 September 2026. Keep the `gh-pages`
artifact branch and Website/release workflows: Cloudflare still depends on them.

Rollback now requires re-enabling and successfully deploying GitHub Pages before
restoring its DNS target; changing DNS alone is not sufficient. Retiring GitHub
Pages also retires the old github.io-hosted URLs and redirects.
