# Contributing to dbterm

Thanks for helping improve dbterm.

This guide is intentionally practical, especially for first-time open-source contributors.

## 1. Before you start

- Check open issues: <https://github.com/shreyam1008/dbterm/issues>
- If your change is large, open an issue first to align scope.
- Keep first contributions small and testable.

## 2. Local setup

```bash
git clone https://github.com/<your-username>/dbterm.git
cd dbterm
```

Build app:

```bash
make build
```

Run tests:

```bash
make test
```

Run website locally:

```bash
cd site
npm install
npm run dev
```

## 3. What to work on first

Good first contribution types:

- Reproducible bug fixes with clear before/after behavior.
- Keyboard-flow polish and help text clarity.
- Connection/setup UX improvements.
- Documentation and guide updates that match app behavior.

## 4. Quality bar for pull requests

- Keep one PR focused on one user outcome.
- Include a short description of the problem and the fix.
- Include verification steps.
- If UI behavior changed, include screenshot or terminal output.
- Update docs when behavior or commands change.

## 5. Commit and PR flow

1. Create a feature branch.
2. Make changes.
3. Run checks (`make test`, site build if docs/site changed).
4. Push branch.
5. Open PR with context and validation notes.

## 6. Documentation sync checklist

When applicable, update these together:

- `README.md`
- `site/src/pages/index.astro`
- `site/src/pages/guide.astro`
- `site/src/pages/open-source.astro`

## 7. Release notes source

The release workflow reads `releases/versions.txt` top entry:

```text
<version>|<release name>|<short description>
```

If your PR ships user-facing behavior, ensure release notes can describe it clearly.
