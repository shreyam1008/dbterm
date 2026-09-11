# dbterm Store copy

Short description: Browse databases, run SQL and manage backups from a keyboard-first terminal workbench.

dbterm is a terminal SQL client and database backup workbench for PostgreSQL, MySQL/MariaDB, SQLite, Turso and Cloudflare D1. Save connections locally, browse schemas and tables, run SQL, inspect results and export data using your keyboard.

Use the backup workbench for supported database backups and guarded restores, with manifests, verification and chosen local destinations. PostgreSQL and MySQL operations can require separately installed database utilities. Cloud connections require your own accounts, endpoints and permissions.

This is a console application. Start dbterm from the Start menu or type dbterm in a Windows terminal. A keyboard is required. Keep the terminal open while running the app or a foreground backup agent.

The Microsoft Store edition updates through Microsoft Store Library and uninstalls through Windows Settings. OS-managed background backup-service registration is unavailable in this edition because Store package paths change on updates. Foreground scheduling is available through dbterm backup agent while its terminal remains open. Use the standalone edition if you need a persistent native OS service.

Free and open source. No dbterm account is required. Website: https://dbterm.shreyam1008.com.np/

Certification: Start with an isolated local SQLite database and non-sensitive test data. No vendor credentials are needed for SQLite. Network databases and external utilities are optional and require tester-owned services. runFullTrust supports console execution, local database/backup files and database utilities. Store lifecycle commands return Store-specific guidance without deleting user data.
