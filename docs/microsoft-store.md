# Microsoft Store

11 September 2026: **dbterm v0.11.1.0 passed Partner Center package validation**, product `9P0RG2ZJ47M8`, Submission 1 `1152921505701871959`. Properties, free pricing and age ratings are saved. Not submitted or published; screenshots and interactive acceptance remain.

Identity: `shreyam1008.dbterm`; family `shreyam1008.dbterm_ax0kgekbzfne6`; publisher `CN=56C87ED7-40E5-4525-B1C3-5F8CD5E02720`, display name `shreyam1008`.

## Package and behavior

Windows Desktop x64 MSIX, minimum Windows 10 build 19041, Start entry and `dbterm.exe` console execution alias. Original `site/public/favicon-512.png` supplies the package and listing images. `runFullTrust` supports database networking, local files and installed database utilities.

The Store edition uses Microsoft Store Library for updates and Windows Settings for uninstall. Its CLI refuses self-update/self-uninstall before touching profiles, backups or tasks. OS-managed backup services are unavailable in the Store edition because MSIX version paths change on updates; keep `dbterm backup agent` running in a terminal for foreground scheduling, or use the standalone edition for native service registration. Database operations and manual backups remain available. External database tools are not bundled.

## Evidence and remaining gates

- Full `go test ./...` passed, including Store-path, config, backup and lifecycle tests.
- Local v0.11.1 candidate built and passed MakeAppx; development registration reports version 0.11.1.0 and Status Ok.
- Registered alias reports v0.11.1. Self-update, self-uninstall and managed-service commands correctly return Store-specific guidance.
- [v0.11.1](https://github.com/shreyam1008/dbterm/releases/tag/v0.11.1) is published from commit `d3e4402`. [Release run 34611977970](https://github.com/shreyam1008/dbterm/actions/runs/34611977970) passed tests, vet, race tests, website verification, all release binaries and APT publishing. The recipe rejects versions before v0.11.1.
- [Automatic Store build 34612837436](https://github.com/shreyam1008/dbterm/actions/runs/34612837436) produced `dbterm_0.11.1.0_x64.msix`. Downloaded SHA-256 `e9bcde83e9c0b2fc8730bfb5a6fafd30f2aba4c345d0357a0d7408a70fed0457` matches its receipt. Microsoft validated and saved this package for Windows Desktop.
- The CI package was unpacked and development-registered locally, replacing only the earlier development candidate. Its registered alias reports v0.11.1/build d3e4402; update, uninstall and service commands all return the expected Store-specific refusal before modifying data. This is not a Store-signed installation test.
- Still required: real terminal UI acceptance and sanitized screenshots, Store-signed installation/update/uninstall acceptance, completed listing and certification. The existing main_ui.png contains production-looking data and is not suitable as fresh Store evidence.

## Automation

`.github/workflows/store.yml` consumes published stable releases, validates the GitHub asset SHA-256, derives MAJOR.MINOR.PATCH.0 and retains the MSIX plus receipt. `workflow_run` also handles releases created by GitHub's built-in token, whose release events do not start another workflow. It only uses a release tagged at the completed release run's commit.

After the first Store publication, configure a `microsoft-store` GitHub environment with `STORE_TENANT_ID`, `STORE_SELLER_ID`, `STORE_CLIENT_ID`, and `STORE_CLIENT_SECRET` secrets for a Partner Center-associated Entra app. Set repository variable `STORE_UPLOAD_ENABLED=true`. The upload script requires a published submission and refuses any pending draft before invoking the Microsoft CLI. No credentials are committed and automatic upload is not yet enabled.

Partner Center has no associated Entra tenant. The owner declined billing/account setup for now. Keep API uploads disabled. Stable-release MSIX artifact generation is verified and works independently of that setup.

[Microsoft Store CLI](https://learn.microsoft.com/en-us/windows/apps/publish/msstore-dev-cli/commands) · [Submission API prerequisites](https://learn.microsoft.com/en-us/windows/uwp/monetize/create-and-manage-submissions-using-windows-store-services)
