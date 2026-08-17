# Product domain and release contract

`dbterm` is the live reference implementation. The other rows are preparation targets; repository changes alone do not make a domain live.

| Product | Canonical URL | Pages source | Status |
| --- | --- | --- | --- |
| dbterm | `https://dbterm.shreyam1008.com.np/` | `gh-pages` (site plus APT metadata) | **Live** |
| GoBarryGo | `https://gobarrygo.shreyam1008.com.np/` | GitHub Actions | **Prepared; deployment and public checks pending** |
| Visualise OKLCH | `https://visualise-oklch.shreyam1008.com.np/` | GitHub Actions | **Prepared; deployment and public checks pending** |
| shre-skills | `https://skills.shreyam1008.com.np/` | GitHub Actions | **Prepared; deployment and public checks pending** |

## Contract

- Production metadata has one HTTPS canonical origin. Canonical links, Open Graph URLs, JSON-LD, `robots.txt`, sitemap entries, README links, and current package metadata must agree.
- GitHub Actions Pages builds take their origin and base path from `actions/configure-pages`; do not hard-code a legacy repository path into a build.
- Provision each new custom subdomain with one **DNS-only** record while GitHub validates DNS and issues TLS. Do not include a URL scheme or repository path:

  ```text
  CNAME gobarrygo.shreyam1008.com.np       -> shreyam1008.github.io
  CNAME visualise-oklch.shreyam1008.com.np -> shreyam1008.github.io
  CNAME skills.shreyam1008.com.np          -> shreyam1008.github.io
  ```

  dbterm was provisioned this way and is now intentionally proxied through Cloudflare for its documented response negotiation and discovery headers. Do not change that working record during the other migrations. Proxy a new product only after its direct GitHub Pages deployment, certificate, cache behavior, and rollback have all been verified.

- A GitHub Actions Pages site does not need a committed `CNAME` file. dbterm is the exception because its existing branch publisher preserves the `CNAME` managed on `gh-pages` alongside APT content.
- A release/store is **Live** only after its public URL works without an authenticated account. Repository manifests alone are **Prepared**, not submitted or published.

## One-domain migration order

Repeat this for one product at a time.

1. Verify ownership of `shreyam1008.com.np` in GitHub and retain its `_github-pages-challenge-...` TXT record.
2. Merge the prepared workflow and confirm a successful Pages deployment at the current legacy URL. Set **Settings → Pages → Build and deployment → Source** to **GitHub Actions** for the three workflow-mode sites; keep dbterm on its existing `gh-pages` source.
3. In **Settings → Pages → Custom domain**, save the target hostname. Do this immediately before DNS so the name is claimed by the intended repository.
4. At the DNS provider, create the exact CNAME above with proxying disabled (**DNS only**). Remove conflicting A, AAAA, ALIAS, ANAME, or CNAME records for that hostname. Never use a wildcard.
5. Wait for GitHub's DNS check and TLS certificate. DNS and certificate provisioning can take up to 24 hours. Enable **Enforce HTTPS** only when GitHub makes it available.
6. Rerun the Pages workflow. This makes `configure-pages` supply the custom origin/base to generated metadata.
7. Run the checks below. Only then change status from **Prepared** to **Live**.

## Public checks

Set `DOMAIN` and `REPO`, for example `DOMAIN=gobarrygo.shreyam1008.com.np` and `REPO=gobarrygo`.

```bash
dig +short CNAME "$DOMAIN"
curl -sSIL "http://$DOMAIN/"
curl -fsSIL "https://$DOMAIN/"
curl -fsSL "https://$DOMAIN/" | rg -F "https://$DOMAIN/"
curl -fsSL "https://$DOMAIN/robots.txt"
curl -fsSL "https://$DOMAIN/sitemap.xml" | rg -F "https://$DOMAIN/"
curl -sSL -o /dev/null -w '%{url_effective} %{http_code}\n' "https://shreyam1008.github.io/$REPO/"
```

Pass criteria: the CNAME resolves to `shreyam1008.github.io.`, HTTP redirects to HTTPS, HTTPS returns 200 without certificate warnings or mixed content, canonical/robots/sitemap use the custom origin, assets and internal links work, and the legacy project URL redirects to the same custom-domain path.

## Rollback

1. Record the failed check and last known-good workflow run.
2. Remove the custom hostname's DNS CNAME first, then remove the repository's Pages custom-domain setting. This avoids leaving a dangling DNS record that another Pages site could claim.
3. Rerun the last known-good Pages workflow and verify the legacy `https://shreyam1008.github.io/<repo>/` URL.
4. Revert canonical/package metadata only if the custom domain is being abandoned, not for a temporary certificate or DNS delay.

Use exact status language: **Prepared; not deployed**, **DNS configured; certificate pending**, **Deployed; verification pending**, **Live; verified on <date/timezone>**, or **Rolled back to legacy Pages URL**.
