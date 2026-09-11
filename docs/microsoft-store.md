# Microsoft Store

11 September 2026: reserved as **dbterm**, product `9P0RG2ZJ47M8`, Submission 1 `1152921505701871959`. Not submitted or published.

Identity: `shreyam1008.dbterm`; family `shreyam1008.dbterm_ax0kgekbzfne6`; publisher `CN=56C87ED7-40E5-4525-B1C3-5F8CD5E02720`, display name `shreyam1008`.

## Package and behavior

Windows Desktop x64 MSIX, minimum Windows 10 build 19041, Start entry and `dbterm.exe` console execution alias. Original `site/public/favicon-512.png` supplies the package and listing images. `runFullTrust` supports database networking, local files and installed database utilities.

The Store edition uses Microsoft Store Library for updates and Windows Settings for uninstall. Its CLI refuses self-update/self-uninstall before touching profiles, backups or tasks. OS-managed backup services are unavailable in the Store edition because MSIX version paths change on updates; keep `dbterm backup agent` running in a terminal for foreground scheduling, or use the standalone edition for native service registration. Database operations and manual backups remain available. External database tools are not bundled.

## Evidence and remaining gates

- Full `go test ./...` passed, including Store-path, config, backup and lifecycle tests.
- Local v0.11.1 candidate built and passed MakeAppx; development registration reports version 0.11.1.0 and Status Ok.
- Registered alias reports v0.11.1. Self-update, self-uninstall and managed-service commands correctly return Store-specific guidance.
- This is an unreleased source candidate, not the unchanged public v0.11.0 binary. Publish a release containing these guards before Store submission. The recipe rejects versions before v0.11.1.
- Still required: real terminal UI acceptance and sanitized screenshots, clean Store installation/update/uninstall, complete Partner Center metadata and age ratings, certification.

## Automation

`.github/workflows/store.yml` consumes published stable releases, validates the GitHub asset SHA-256, derives MAJOR.MINOR.PATCH.0 and retains the MSIX plus receipt. `workflow_run` also handles releases created by GitHub's built-in token, whose release events do not start another workflow. It only uses a release tagged at the completed release run's commit.

After the first Store publication, configure a `microsoft-store` GitHub environment with `STORE_TENANT_ID`, `STORE_SELLER_ID`, `STORE_CLIENT_ID`, and `STORE_CLIENT_SECRET` secrets for a Partner Center-associated Entra app. Set repository variable `STORE_UPLOAD_ENABLED=true`. The upload script requires a published submission and refuses any pending draft before invoking the Microsoft CLI. No credentials are committed and automatic upload is not yet enabled.

[Microsoft Store CLI](https://learn.microsoft.com/en-us/windows/apps/publish/msstore-dev-cli/commands) · [Submission API prerequisites](https://learn.microsoft.com/en-us/windows/uwp/monetize/create-and-manage-submissions-using-windows-store-services)
