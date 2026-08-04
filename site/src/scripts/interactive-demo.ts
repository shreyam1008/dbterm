import type {
  Database,
  Sqlite3Static,
  SqlValue
} from "@sqlite.org/sqlite-wasm";

type DemoValue = SqlValue;

interface QueryResult {
  columns: string[];
  rows: DemoValue[][];
  truncated: boolean;
  elapsedMs: number;
}

interface ResultFilter {
  column: string;
  operator: "contains" | "equals" | "starts" | "greater" | "less";
  value: string;
}

interface HistoryEntry {
  sql: string;
  table: string | null;
}

interface PaletteAction {
  id: string;
  label: string;
  description: string;
  keywords: string;
  run: () => void | Promise<void>;
}

interface ScoredAction {
  action: PaletteAction;
  score: number;
}

const DEFAULT_QUERY = `SELECT
  u.name AS customer,
  o.id AS order_id,
  p.name AS product,
  o.quantity,
  printf('$%.2f', o.total) AS total,
  o.status
FROM orders AS o
JOIN users AS u ON u.id = o.user_id
JOIN products AS p ON p.id = o.product_id
ORDER BY o.created_at DESC
LIMIT 8;`;

const TABLE_QUERIES: Record<string, string> = {
  users: "SELECT id, name, email, plan, status, created_at FROM users ORDER BY id;",
  orders:
    "SELECT id, user_id, product_id, status, quantity, total, created_at FROM orders ORDER BY created_at DESC;",
  payments:
    "SELECT id, order_id, amount, method, status, paid_at FROM payments ORDER BY paid_at DESC;",
  products:
    "SELECT id, name, category, price, inventory FROM products ORDER BY id;"
};

const FIXTURE_SQL = `
PRAGMA foreign_keys = ON;

CREATE TABLE users (
  id INTEGER PRIMARY KEY,
  name TEXT NOT NULL,
  email TEXT NOT NULL UNIQUE,
  plan TEXT NOT NULL,
  status TEXT NOT NULL,
  created_at TEXT NOT NULL
);

CREATE TABLE products (
  id INTEGER PRIMARY KEY,
  name TEXT NOT NULL,
  category TEXT NOT NULL,
  price REAL NOT NULL,
  inventory INTEGER NOT NULL
);

CREATE TABLE orders (
  id INTEGER PRIMARY KEY,
  user_id INTEGER NOT NULL REFERENCES users(id),
  product_id INTEGER NOT NULL REFERENCES products(id),
  status TEXT NOT NULL,
  quantity INTEGER NOT NULL,
  total REAL NOT NULL,
  created_at TEXT NOT NULL
);

CREATE TABLE payments (
  id INTEGER PRIMARY KEY,
  order_id INTEGER NOT NULL UNIQUE REFERENCES orders(id),
  amount REAL NOT NULL,
  method TEXT NOT NULL,
  status TEXT NOT NULL,
  paid_at TEXT NOT NULL
);

INSERT INTO users VALUES
  (101, 'Maya Chen', 'maya@northstar.dev', 'Pro', 'active', '2026-07-18'),
  (102, 'Noah Williams', 'noah@atlas.studio', 'Team', 'active', '2026-07-15'),
  (103, 'Priya Nair', 'priya@paperkite.io', 'Pro', 'active', '2026-07-11'),
  (104, 'Theo Martin', 'theo@pinelabs.co', 'Free', 'trial', '2026-07-08'),
  (105, 'Sofia Garcia', 'sofia@cobalt.dev', 'Team', 'active', '2026-06-29'),
  (106, 'Eli Brooks', 'eli@relay.systems', 'Pro', 'paused', '2026-06-22'),
  (107, 'Aisha Rahman', 'aisha@tempo.app', 'Team', 'active', '2026-06-14'),
  (108, 'Jon Bell', 'jon@wildflower.design', 'Free', 'active', '2026-06-04');

INSERT INTO products VALUES
  (201, 'Query Studio', 'Developer tools', 29.00, 84),
  (202, 'Data Canvas', 'Analytics', 49.00, 36),
  (203, 'Schema Lens', 'Developer tools', 19.00, 112),
  (204, 'Team Console', 'Collaboration', 79.00, 21),
  (205, 'Audit Stream', 'Security', 59.00, 48),
  (206, 'Edge Sync', 'Infrastructure', 39.00, 65);

INSERT INTO orders VALUES
  (4001, 101, 202, 'paid', 1, 49.00, '2026-08-03 14:22:08'),
  (4002, 107, 204, 'paid', 2, 158.00, '2026-08-03 12:08:31'),
  (4003, 103, 201, 'processing', 1, 29.00, '2026-08-02 18:44:19'),
  (4004, 102, 205, 'paid', 1, 59.00, '2026-08-02 10:17:02'),
  (4005, 105, 206, 'paid', 3, 117.00, '2026-08-01 16:03:47'),
  (4006, 104, 203, 'pending', 1, 19.00, '2026-08-01 09:51:24'),
  (4007, 108, 201, 'refunded', 1, 29.00, '2026-07-31 20:40:13'),
  (4008, 106, 202, 'paid', 1, 49.00, '2026-07-31 13:28:55'),
  (4009, 103, 205, 'processing', 2, 118.00, '2026-07-30 19:11:06'),
  (4010, 101, 203, 'paid', 2, 38.00, '2026-07-29 08:35:41');

INSERT INTO payments VALUES
  (7001, 4001, 49.00, 'card', 'captured', '2026-08-03 14:22:11'),
  (7002, 4002, 158.00, 'bank', 'captured', '2026-08-03 12:08:36'),
  (7003, 4003, 29.00, 'card', 'authorized', '2026-08-02 18:44:23'),
  (7004, 4004, 59.00, 'wallet', 'captured', '2026-08-02 10:17:05'),
  (7005, 4005, 117.00, 'bank', 'captured', '2026-08-01 16:03:51'),
  (7006, 4006, 19.00, 'card', 'pending', '2026-08-01 09:51:31'),
  (7007, 4007, 29.00, 'card', 'refunded', '2026-07-31 20:41:02'),
  (7008, 4008, 49.00, 'wallet', 'captured', '2026-07-31 13:29:01'),
  (7009, 4009, 118.00, 'bank', 'authorized', '2026-07-30 19:11:14'),
  (7010, 4010, 38.00, 'card', 'captured', '2026-07-29 08:35:46');
`;

const TABLE_COUNTS: Record<string, number> = {
  users: 8,
  orders: 10,
  payments: 10,
  products: 6
};

const FK_TARGETS: Record<string, { table: string; column: string }> = {
  user_id: { table: "users", column: "id" },
  order_id: { table: "orders", column: "id" },
  product_id: { table: "products", column: "id" }
};

const SAFE_PRAGMAS = new Set([
  "collation_list",
  "compile_options",
  "database_list",
  "foreign_key_list",
  "function_list",
  "index_info",
  "index_list",
  "index_xinfo",
  "module_list",
  "pragma_list",
  "table_info",
  "table_list",
  "table_xinfo"
]);

const query = <T extends Element>(root: ParentNode, selector: string): T => {
  const element = root.querySelector<T>(selector);
  if (!element) throw new Error(`Interactive demo is missing ${selector}`);
  return element;
};

const printable = (value: DemoValue): string => {
  if (value === null) return "NULL";
  if (value instanceof Uint8Array || value instanceof Int8Array) {
    return `BLOB · ${value.byteLength} B`;
  }
  if (value instanceof ArrayBuffer) return `BLOB · ${value.byteLength} B`;
  return String(value);
};

const csvValue = (value: DemoValue): string => {
  if (value === null) return "";
  const plain = printable(value);
  return /[",\r\n]/.test(plain) ? `"${plain.replaceAll('"', '""')}"` : plain;
};

const asErrorMessage = (error: unknown): string =>
  error instanceof Error ? error.message : "Something unexpected happened.";

const isTypingTarget = (target: EventTarget | null): boolean => {
  if (!(target instanceof HTMLElement)) return false;
  return (
    target instanceof HTMLInputElement ||
    target instanceof HTMLTextAreaElement ||
    target instanceof HTMLSelectElement ||
    target.isContentEditable
  );
};

const normalizedForFilter = (value: DemoValue): string =>
  value === null ? "null" : printable(value).toLocaleLowerCase();

const scoreAction = (action: PaletteAction, rawNeedle: string): number => {
  const needle = rawNeedle.trim().toLocaleLowerCase();
  if (!needle) return 1;
  const haystack = `${action.label} ${action.description} ${action.keywords}`.toLocaleLowerCase();
  if (haystack.includes(needle)) {
    return 200 - haystack.indexOf(needle) - needle.length * 0.2;
  }

  let needleIndex = 0;
  let gapPenalty = 0;
  let previousMatch = -1;
  for (let index = 0; index < haystack.length && needleIndex < needle.length; index += 1) {
    if (haystack[index] !== needle[needleIndex]) continue;
    if (previousMatch >= 0) gapPenalty += Math.max(0, index - previousMatch - 1);
    previousMatch = index;
    needleIndex += 1;
  }

  return needleIndex === needle.length ? 100 - gapPenalty : Number.NEGATIVE_INFINITY;
};

const appendHighlighted = (container: HTMLElement, label: string, rawNeedle: string): void => {
  const needle = rawNeedle.trim().toLocaleLowerCase();
  if (!needle) {
    container.textContent = label;
    return;
  }

  const lowerLabel = label.toLocaleLowerCase();
  const directIndex = lowerLabel.indexOf(needle);
  if (directIndex >= 0) {
    container.append(document.createTextNode(label.slice(0, directIndex)));
    const mark = document.createElement("mark");
    mark.textContent = label.slice(directIndex, directIndex + needle.length);
    container.append(mark, document.createTextNode(label.slice(directIndex + needle.length)));
    return;
  }

  let needleIndex = 0;
  for (const character of label) {
    if (needleIndex < needle.length && character.toLocaleLowerCase() === needle[needleIndex]) {
      const mark = document.createElement("mark");
      mark.textContent = character;
      container.append(mark);
      needleIndex += 1;
    } else {
      container.append(document.createTextNode(character));
    }
  }
};

class DbtermDemo {
  private readonly root: HTMLElement;
  private readonly editor: HTMLTextAreaElement;
  private readonly runButton: HTMLButtonElement;
  private readonly resetButton: HTMLButtonElement;
  private readonly resultHead: HTMLTableSectionElement;
  private readonly resultBody: HTMLTableSectionElement;
  private readonly resultTable: HTMLTableElement;
  private readonly emptyState: HTMLElement;
  private readonly status: HTMLElement;
  private readonly rowCount: HTMLElement;
  private readonly duration: HTMLElement;
  private readonly filterChip: HTMLElement;
  private readonly engineVersion: HTMLElement;
  private readonly paletteDialog: HTMLDialogElement;
  private readonly paletteInput: HTMLInputElement;
  private readonly paletteList: HTMLElement;
  private readonly filterDialog: HTMLDialogElement;
  private readonly filterForm: HTMLFormElement;
  private readonly filterColumn: HTMLSelectElement;
  private readonly filterOperator: HTMLSelectElement;
  private readonly filterValue: HTMLInputElement;
  private readonly helpDialog: HTMLDialogElement;
  private sqlite: Sqlite3Static | null = null;
  private database: Database | null = null;
  private databasePromise: Promise<Database> | null = null;
  private result: QueryResult = { columns: [], rows: [], truncated: false, elapsedMs: 0 };
  private filter: ResultFilter | null = null;
  private selectedRow = 0;
  private selectedColumn = 0;
  private selectedTable: string | null = null;
  private history: HistoryEntry[] = [];
  private copiedValue = "";
  private requestId = 0;
  private loadingTimer: number | null = null;
  private isLoading = false;
  private lazyObserver: IntersectionObserver | null = null;
  private paletteActions: PaletteAction[] = [];
  private visiblePaletteActions: PaletteAction[] = [];
  private paletteIndex = 0;
  private readonly focusReturn = new WeakMap<HTMLDialogElement, HTMLElement>();

  constructor(root: HTMLElement) {
    this.root = root;
    this.editor = query(root, "[data-demo-editor]");
    this.runButton = query(root, "[data-demo-run]");
    this.resetButton = query(root, "[data-demo-reset]");
    this.resultHead = query(root, "[data-demo-result-head]");
    this.resultBody = query(root, "[data-demo-result-body]");
    this.resultTable = query(root, "[data-demo-result-table]");
    this.emptyState = query(root, "[data-demo-empty]");
    this.status = query(root, "[data-demo-status]");
    this.rowCount = query(root, "[data-demo-row-count]");
    this.duration = query(root, "[data-demo-duration]");
    this.filterChip = query(root, "[data-demo-filter-chip]");
    this.engineVersion = query(root, "[data-demo-engine-version]");
    this.paletteDialog = query(root, "[data-demo-palette-dialog]");
    this.paletteInput = query(root, "[data-demo-palette-input]");
    this.paletteList = query(root, "[data-demo-palette-list]");
    this.filterDialog = query(root, "[data-demo-filter-dialog]");
    this.filterForm = query(root, "[data-demo-filter-form]");
    this.filterColumn = query(root, "[data-demo-filter-column]");
    this.filterOperator = query(root, "[data-demo-filter-operator]");
    this.filterValue = query(root, "[data-demo-filter-value]");
    this.helpDialog = query(root, "[data-demo-help-dialog]");
    this.editor.value = DEFAULT_QUERY;
    this.paletteActions = this.createPaletteActions();
    this.bindEvents();
    this.updateTableCounts();
    this.renderEmpty();
    this.armLazyBoot();
  }

  private bindEvents(): void {
    this.runButton.addEventListener("click", () => void this.runQuery());
    this.resetButton.addEventListener("click", () => void this.reset());

    this.root.querySelectorAll<HTMLButtonElement>("[data-demo-table]").forEach((button) => {
      button.addEventListener("click", () => {
        const table = button.dataset.demoTable;
        if (table) void this.openTable(table, true);
      });
    });

    this.root.querySelectorAll<HTMLButtonElement>("[data-demo-open-palette]").forEach((button) => {
      button.addEventListener("click", () => this.openPalette(button));
    });
    this.root.querySelectorAll<HTMLButtonElement>("[data-demo-open-filter]").forEach((button) => {
      button.addEventListener("click", () => this.openFilter(button));
    });
    this.root.querySelectorAll<HTMLButtonElement>("[data-demo-export]").forEach((button) => {
      button.addEventListener("click", () => this.exportCsv());
    });
    this.root.querySelectorAll<HTMLButtonElement>("[data-demo-open-help]").forEach((button) => {
      button.addEventListener("click", () => this.openHelp(button));
    });
    this.root.querySelectorAll<HTMLButtonElement>("[data-demo-clear-filter]").forEach((button) => {
      button.addEventListener("click", () => this.clearFilter());
    });

    this.editor.addEventListener("keydown", (event) => {
      if ((event.ctrlKey || event.metaKey) && event.key === "Enter") {
        event.preventDefault();
        void this.runQuery();
      }
    });

    this.resultBody.addEventListener("click", (event) => {
      const cell = (event.target as Element).closest<HTMLButtonElement>("[data-demo-cell]");
      if (cell) this.selectCell(cell, true);
    });
    this.resultBody.addEventListener("keydown", (event) => this.handleCellArrows(event));

    this.root.addEventListener("keydown", (event) => this.handleRootShortcuts(event));

    this.paletteInput.addEventListener("input", () => this.renderPalette());
    this.paletteInput.addEventListener("keydown", (event) => this.handlePaletteKeys(event));
    this.paletteList.addEventListener("pointermove", (event) => {
      const option = (event.target as Element).closest<HTMLElement>("[role='option']");
      if (!option) return;
      const index = Number(option.dataset.paletteIndex);
      if (Number.isFinite(index)) this.setPaletteIndex(index, false);
    });
    this.paletteList.addEventListener("click", (event) => {
      const option = (event.target as Element).closest<HTMLElement>("[role='option']");
      if (!option) return;
      const index = Number(option.dataset.paletteIndex);
      if (Number.isFinite(index)) void this.choosePaletteAction(index);
    });

    this.filterForm.addEventListener("submit", (event) => {
      event.preventDefault();
      this.applyFilter({
        column: this.filterColumn.value,
        operator: this.filterOperator.value as ResultFilter["operator"],
        value: this.filterValue.value
      });
      this.filterDialog.close();
    });
    query<HTMLButtonElement>(this.filterDialog, "[data-demo-filter-reset]").addEventListener(
      "click",
      () => {
        this.clearFilter();
        this.filterDialog.close();
      }
    );
    query<HTMLButtonElement>(this.filterDialog, "[data-demo-filter-close]").addEventListener(
      "click",
      () => this.filterDialog.close()
    );

    this.root.querySelectorAll<HTMLDialogElement>("dialog").forEach((dialog) => {
      dialog.addEventListener("click", (event) => {
        if (event.target === dialog) dialog.close();
      });
      dialog.addEventListener("close", () => {
        const target = this.focusReturn.get(dialog);
        if (target?.isConnected) target.focus();
      });
    });
  }

  private createPaletteActions(): PaletteAction[] {
    return [
      {
        id: "run",
        label: "Run current query",
        description: "Execute the SQL in the editor with SQLite WASM",
        keywords: "execute select ctrl enter",
        run: () => this.runQuery()
      },
      ...Object.keys(TABLE_QUERIES).map((table) => ({
        id: `table-${table}`,
        label: `Open ${table}`,
        description: `Browse all ${TABLE_COUNTS[table]} rows in the ${table} table`,
        keywords: `database table select ${table}`,
        run: () => this.openTable(table, true)
      })),
      {
        id: "schema-users",
        label: "Inspect users schema",
        description: "Run PRAGMA table_info('users')",
        keywords: "columns schema pragma metadata",
        run: () => {
          this.pushHistory();
          this.selectedTable = "users";
          this.editor.value = "PRAGMA table_info('users');";
          return this.runQuery();
        }
      },
      {
        id: "revenue",
        label: "Revenue by product",
        description: "Aggregate captured payments across products",
        keywords: "sum group analytics payment sales",
        run: () => {
          this.pushHistory();
          this.selectedTable = null;
          this.editor.value = `SELECT p.name AS product,
  COUNT(*) AS orders,
  printf('$%.2f', SUM(pay.amount)) AS revenue
FROM payments AS pay
JOIN orders AS o ON o.id = pay.order_id
JOIN products AS p ON p.id = o.product_id
WHERE pay.status = 'captured'
GROUP BY p.id
ORDER BY SUM(pay.amount) DESC;`;
          return this.runQuery();
        }
      },
      {
        id: "filter",
        label: "Filter current results",
        description: "Choose a column, operator, and value",
        keywords: "find slash search rows",
        run: () => this.openFilter(this.runButton)
      },
      {
        id: "export",
        label: "Export visible rows",
        description: "Download the current result scope as CSV",
        keywords: "csv download save alt e",
        run: () => this.exportCsv()
      },
      {
        id: "help",
        label: "Show keyboard guide",
        description: "See every shortcut available in this tour",
        keywords: "commands shortcuts keys alt h",
        run: () => this.openHelp(this.runButton)
      },
      {
        id: "reset",
        label: "Reset sample database",
        description: "Rebuild the private in-memory SQLite fixture",
        keywords: "restore reload data",
        run: () => this.reset()
      }
    ];
  }

  private async ensureDatabase(): Promise<Database> {
    if (this.database?.isOpen()) return this.database;
    if (this.databasePromise) return this.databasePromise;

    this.setEngineState("loading", "Loading SQLite WASM…");
    this.databasePromise = (async () => {
      const { default: initializeSQLite } = await import("@sqlite.org/sqlite-wasm");
      this.sqlite ??= await initializeSQLite();
      const db = new this.sqlite.oo1.DB(":memory:", "c");
      db.exec(FIXTURE_SQL);
      db.exec("PRAGMA query_only = ON;");
      this.database = db;
      this.engineVersion.textContent = `SQLite ${this.sqlite.version.libVersion}`;
      this.setEngineState("ready", "SQLite ready");
      return db;
    })();

    try {
      return await this.databasePromise;
    } catch (error) {
      this.databasePromise = null;
      this.setEngineState("error", "SQLite could not load");
      throw error;
    }
  }

  private armLazyBoot(): void {
    if (!("IntersectionObserver" in window)) return;
    this.lazyObserver = new IntersectionObserver(
      (entries) => {
        if (!entries.some((entry) => entry.isIntersecting)) return;
        this.lazyObserver?.disconnect();
        this.lazyObserver = null;
        void this.runQuery();
      },
      { rootMargin: "140px 0px", threshold: 0.05 }
    );
    this.lazyObserver.observe(this.root);
  }

  private execute(sql: string, database: Database): QueryResult {
    if (!this.sqlite) throw new Error("SQLite has not initialized.");
    const cleanSql = sql.trim();
    if (!cleanSql) throw new Error("Write a SELECT or PRAGMA query first.");
    if (cleanSql.length > 4_000) throw new Error("Keep playground queries under 4,000 characters.");
    const withoutTrailingSemicolon = cleanSql.replace(/;\s*$/, "");
    if (withoutTrailingSemicolon.includes(";")) {
      throw new Error("The playground runs one read-only statement at a time.");
    }

    const statementHead = withoutTrailingSemicolon
      .replace(/^\s*(?:(?:--[^\n]*(?:\n|$))|(?:\/\*[\s\S]*?\*\/\s*))*/, "")
      .trim();
    if (/^pragma\b/i.test(statementHead)) {
      const pragma = statementHead.match(
        /^pragma\s+(?:(?:main|temp)\s*\.\s*)?([a-z_][a-z0-9_]*)\s*(.*)$/i
      );
      const name = pragma?.[1]?.toLocaleLowerCase() ?? "";
      const argument = pragma?.[2]?.trim() ?? "";
      const isReadOnlyShape = argument === "" || /^\([^)]*\)$/.test(argument);
      if (!SAFE_PRAGMAS.has(name) || !isReadOnlyShape) {
        throw new Error(
          "That PRAGMA changes connection state. This tour permits read-only schema PRAGMAs only."
        );
      }
    }

    const started = performance.now();
    const statement = database.prepare(cleanSql);
    let progressChecks = 0;
    try {
      if (!this.sqlite.capi.sqlite3_stmt_readonly(statement)) {
        throw new Error("This product tour is read-only. Use Reset to restore the sample data.");
      }
      if (statement.columnCount === 0) {
        throw new Error("Use a statement that returns rows, such as SELECT, WITH, EXPLAIN, or PRAGMA.");
      }

      this.sqlite.capi.sqlite3_progress_handler(
        database,
        1_000,
        () => {
          progressChecks += 1;
          return progressChecks > 10_000 ? 1 : 0;
        },
        0 as never
      );

      const columns = statement.getColumnNames([]);
      const rows: DemoValue[][] = [];
      while (rows.length < 100 && statement.step()) rows.push(statement.get([]));
      const truncated = rows.length === 100 && statement.step();
      return {
        columns,
        rows,
        truncated,
        elapsedMs: performance.now() - started
      };
    } finally {
      this.sqlite.capi.sqlite3_progress_handler(database, 0, 0 as never, 0 as never);
      statement.finalize();
      database.exec("PRAGMA query_only = ON;");
    }
  }

  private async runQuery(): Promise<void> {
    this.lazyObserver?.disconnect();
    this.lazyObserver = null;
    if (this.isLoading) this.cancelQuery();
    const currentRequest = ++this.requestId;
    const sql = this.editor.value;
    this.setLoading(true, "Preparing query… Press Ctrl+C to cancel.");

    try {
      const database = await this.ensureDatabase();
      if (currentRequest !== this.requestId) return;

      await new Promise<void>((resolve) => {
        this.loadingTimer = window.setTimeout(resolve, 360);
      });
      this.loadingTimer = null;
      if (currentRequest !== this.requestId) return;

      this.result = this.execute(sql, database);
      this.filter = null;
      this.selectedRow = 0;
      this.selectedColumn = 0;
      this.renderResult();
      const qualifier = this.result.truncated ? " (first 100)" : "";
      this.announce(
        `${this.result.rows.length} row${this.result.rows.length === 1 ? "" : "s"}${qualifier} in ${this.result.elapsedMs.toFixed(1)} milliseconds.`
      );
    } catch (error) {
      if (currentRequest !== this.requestId) return;
      this.renderError(asErrorMessage(error));
    } finally {
      if (currentRequest === this.requestId) this.setLoading(false);
    }
  }

  private cancelQuery(): void {
    if (!this.isLoading) return;
    this.requestId += 1;
    if (this.loadingTimer !== null) window.clearTimeout(this.loadingTimer);
    this.loadingTimer = null;
    this.setLoading(false);
    this.announce("Query cancelled. Your SQL is still in the editor.");
  }

  private async reset(): Promise<void> {
    this.cancelQuery();
    this.database?.close();
    this.database = null;
    this.databasePromise = null;
    this.sqlite = null;
    this.history = [];
    this.filter = null;
    this.selectedTable = null;
    this.editor.value = DEFAULT_QUERY;
    this.renderEmpty("Rebuilding the private sample database…");
    await this.runQuery();
    this.announce("Sample database reset. The original product-tour query is ready.");
  }

  private pushHistory(): void {
    const sql = this.editor.value.trim();
    const previous = this.history.at(-1);
    if (!sql || (previous?.sql === sql && previous.table === this.selectedTable)) return;
    this.history.push({ sql, table: this.selectedTable });
    if (this.history.length > 20) this.history.shift();
  }

  private async navigateBack(): Promise<void> {
    const previous = this.history.pop();
    if (!previous) {
      this.announce("No earlier table in this tour yet.");
      return;
    }
    this.editor.value = previous.sql;
    this.selectedTable = previous.table;
    await this.runQuery();
  }

  private async openTable(table: string, addHistory: boolean): Promise<void> {
    const sql = TABLE_QUERIES[table];
    if (!sql) return;
    if (addHistory) this.pushHistory();
    this.selectedTable = table;
    this.editor.value = sql;
    this.updateTableSelection();
    await this.runQuery();
  }

  private async followSelectedForeignKey(): Promise<void> {
    const value = this.selectedValue();
    const column = this.result.columns[this.selectedColumn];
    const target = FK_TARGETS[column];
    if (!target || value === undefined || value === null) {
      this.announce("Select a user_id, order_id, or product_id cell to follow it.");
      return;
    }
    this.pushHistory();
    this.selectedTable = target.table;
    const numericValue = typeof value === "number" || typeof value === "bigint";
    const literal = numericValue ? String(value) : `'${String(value).replaceAll("'", "''")}'`;
    this.editor.value = `SELECT * FROM ${target.table} WHERE ${target.column} = ${literal};`;
    this.updateTableSelection();
    await this.runQuery();
  }

  private visibleRows(): DemoValue[][] {
    if (!this.filter) return this.result.rows;
    const columnIndex = this.result.columns.indexOf(this.filter.column);
    if (columnIndex < 0) return this.result.rows;
    const needle = this.filter.value.toLocaleLowerCase();
    return this.result.rows.filter((row) => {
      const haystack = normalizedForFilter(row[columnIndex]);
      switch (this.filter?.operator) {
        case "equals":
          return haystack === needle;
        case "starts":
          return haystack.startsWith(needle);
        case "greater": {
          const left = Number(row[columnIndex]);
          const right = Number(this.filter.value);
          return Number.isFinite(left) && Number.isFinite(right) && left > right;
        }
        case "less": {
          const left = Number(row[columnIndex]);
          const right = Number(this.filter.value);
          return Number.isFinite(left) && Number.isFinite(right) && left < right;
        }
        default:
          return haystack.includes(needle);
      }
    });
  }

  private applyFilter(filter: ResultFilter): void {
    if (!this.result.columns.includes(filter.column)) return;
    this.filter = filter;
    this.selectedRow = 0;
    this.selectedColumn = Math.max(0, this.result.columns.indexOf(filter.column));
    this.renderResult();
    this.announce(`${this.visibleRows().length} visible rows after filtering ${filter.column}.`);
  }

  private clearFilter(): void {
    if (!this.filter) return;
    this.filter = null;
    this.selectedRow = 0;
    this.renderResult();
    this.announce(`Filter cleared. ${this.result.rows.length} rows visible.`);
  }

  private selectedValue(): DemoValue | undefined {
    return this.visibleRows()[this.selectedRow]?.[this.selectedColumn];
  }

  private async copySelectedCell(): Promise<void> {
    const value = this.selectedValue();
    if (value === undefined) {
      this.announce("Run a query and select a cell before copying.");
      return;
    }
    const text = value === null ? "NULL" : printable(value);
    this.copiedValue = text;
    try {
      await navigator.clipboard.writeText(text);
      this.announce(`Copied ${text} to the clipboard.`);
    } catch {
      this.announce(`Selected value: ${text}. Clipboard permission was unavailable.`);
    }
  }

  private async filterWithClipboard(): Promise<void> {
    const column = this.result.columns[this.selectedColumn];
    if (!column) {
      this.announce("Select a result cell before filtering from the clipboard.");
      return;
    }
    let clipboard = this.copiedValue;
    try {
      const liveClipboard = await navigator.clipboard.readText();
      if (liveClipboard) clipboard = liveClipboard;
    } catch {
      // Browsers may deny clipboard reads. The last copied demo cell remains available.
    }
    if (!clipboard) {
      this.announce("The clipboard is empty. Press C on a selected cell first.");
      return;
    }
    this.applyFilter({ column, operator: "equals", value: clipboard });
  }

  private renderResult(): void {
    const rows = this.visibleRows();
    delete this.emptyState.dataset.state;
    this.resultHead.replaceChildren();
    this.resultBody.replaceChildren();
    this.emptyState.hidden = true;
    this.resultTable.hidden = false;

    const headRow = document.createElement("tr");
    this.result.columns.forEach((column) => {
      const header = document.createElement("th");
      header.scope = "col";
      header.textContent = column;
      headRow.append(header);
    });
    this.resultHead.append(headRow);

    rows.forEach((row, rowIndex) => {
      const tableRow = document.createElement("tr");
      row.forEach((value, columnIndex) => {
        const cell = document.createElement("td");
        const button = document.createElement("button");
        button.type = "button";
        button.dataset.demoCell = "";
        button.dataset.row = String(rowIndex);
        button.dataset.column = String(columnIndex);
        button.tabIndex = rowIndex === this.selectedRow && columnIndex === this.selectedColumn ? 0 : -1;
        button.textContent = printable(value);
        if (value === null) button.classList.add("is-null");
        if (FK_TARGETS[this.result.columns[columnIndex]]) button.classList.add("is-linkable");
        if (rowIndex === this.selectedRow && columnIndex === this.selectedColumn) {
          button.dataset.selected = "true";
          button.setAttribute("aria-label", `${this.result.columns[columnIndex]}, ${printable(value)}, selected`);
        } else {
          button.setAttribute("aria-label", `${this.result.columns[columnIndex]}, ${printable(value)}`);
        }
        cell.append(button);
        tableRow.append(cell);
      });
      this.resultBody.append(tableRow);
    });

    if (rows.length === 0) {
      const row = document.createElement("tr");
      const cell = document.createElement("td");
      cell.colSpan = Math.max(1, this.result.columns.length);
      cell.className = "demo-no-matches";
      cell.textContent = "No rows match this filter.";
      row.append(cell);
      this.resultBody.append(row);
    }

    this.rowCount.textContent = `${rows.length}${this.result.truncated && !this.filter ? "+" : ""} rows`;
    this.duration.textContent = `${this.result.elapsedMs.toFixed(1)} ms`;
    this.renderFilterChip();
    this.updateTableSelection();
  }

  private renderEmpty(message = "Run the query to wake the in-browser SQLite engine."): void {
    delete this.emptyState.dataset.state;
    this.resultHead.replaceChildren();
    this.resultBody.replaceChildren();
    this.resultTable.hidden = true;
    this.emptyState.hidden = false;
    query<HTMLElement>(this.emptyState, "[data-demo-empty-title]").textContent = "SQLite is standing by";
    query<HTMLElement>(this.emptyState, "[data-demo-empty-message]").textContent = message;
    this.rowCount.textContent = "— rows";
    this.duration.textContent = "— ms";
    this.filterChip.hidden = true;
  }

  private renderError(message: string): void {
    this.resultHead.replaceChildren();
    this.resultBody.replaceChildren();
    this.resultTable.hidden = true;
    this.emptyState.hidden = false;
    this.emptyState.dataset.state = "error";
    query<HTMLElement>(this.emptyState, "[data-demo-empty-title]").textContent = "SQLite returned an error";
    query<HTMLElement>(this.emptyState, "[data-demo-empty-message]").textContent = message;
    this.rowCount.textContent = "0 rows";
    this.duration.textContent = "— ms";
    this.filterChip.hidden = true;
    this.announce(`Query error: ${message}`);
  }

  private renderFilterChip(): void {
    const text = query<HTMLElement>(this.filterChip, "[data-demo-filter-text]");
    if (!this.filter) {
      this.filterChip.hidden = true;
      return;
    }
    const operatorLabels: Record<ResultFilter["operator"], string> = {
      contains: "contains",
      equals: "=",
      starts: "starts with",
      greater: ">",
      less: "<"
    };
    text.textContent = `${this.filter.column} ${operatorLabels[this.filter.operator]} “${this.filter.value}”`;
    this.filterChip.hidden = false;
  }

  private selectCell(cell: HTMLButtonElement, focus: boolean): void {
    this.selectedRow = Number(cell.dataset.row ?? 0);
    this.selectedColumn = Number(cell.dataset.column ?? 0);
    this.resultBody.querySelectorAll<HTMLButtonElement>("[data-demo-cell]").forEach((button) => {
      const selected = button === cell;
      button.tabIndex = selected ? 0 : -1;
      if (selected) button.dataset.selected = "true";
      else delete button.dataset.selected;
    });
    if (focus) cell.focus();
  }

  private handleCellArrows(event: KeyboardEvent): void {
    if (!event.key.startsWith("Arrow")) return;
    const rows = this.visibleRows();
    if (!rows.length || !this.result.columns.length) return;
    event.preventDefault();
    if (event.key === "ArrowLeft") this.selectedColumn -= 1;
    if (event.key === "ArrowRight") this.selectedColumn += 1;
    if (event.key === "ArrowUp") this.selectedRow -= 1;
    if (event.key === "ArrowDown") this.selectedRow += 1;
    this.selectedColumn = Math.max(0, Math.min(this.result.columns.length - 1, this.selectedColumn));
    this.selectedRow = Math.max(0, Math.min(rows.length - 1, this.selectedRow));
    const next = this.resultBody.querySelector<HTMLButtonElement>(
      `[data-demo-cell][data-row="${this.selectedRow}"][data-column="${this.selectedColumn}"]`
    );
    if (next) this.selectCell(next, true);
  }

  private handleRootShortcuts(event: KeyboardEvent): void {
    if (!this.root.contains(document.activeElement)) return;
    const key = event.key.toLocaleLowerCase();

    if ((event.ctrlKey || event.metaKey) && key === "p") {
      event.preventDefault();
      this.openPalette(document.activeElement as HTMLElement);
      return;
    }
    if ((event.ctrlKey || event.metaKey) && key === "c" && this.isLoading) {
      event.preventDefault();
      this.cancelQuery();
      return;
    }
    if (event.altKey && key === "e") {
      event.preventDefault();
      this.exportCsv();
      return;
    }
    if (event.altKey && key === "h") {
      event.preventDefault();
      this.openHelp(document.activeElement as HTMLElement);
      return;
    }
    if (this.paletteDialog.open || this.filterDialog.open || this.helpDialog.open) return;
    if (isTypingTarget(event.target)) return;

    if (event.key === "/") {
      event.preventDefault();
      this.openFilter(document.activeElement as HTMLElement);
    } else if (key === "c" && !event.ctrlKey && !event.metaKey && !event.altKey) {
      event.preventDefault();
      void this.copySelectedCell();
    } else if (key === "v" && !event.ctrlKey && !event.metaKey && !event.altKey) {
      event.preventDefault();
      void this.filterWithClipboard();
    } else if (key === "f" && !event.ctrlKey && !event.metaKey && !event.altKey) {
      event.preventDefault();
      void this.followSelectedForeignKey();
    } else if (event.key === "Backspace") {
      event.preventDefault();
      void this.navigateBack();
    }
  }

  private openPalette(opener: HTMLElement): void {
    if (this.paletteDialog.open) return;
    this.focusReturn.set(this.paletteDialog, opener);
    this.paletteInput.value = "";
    this.paletteIndex = 0;
    this.renderPalette();
    this.paletteDialog.showModal();
    window.setTimeout(() => this.paletteInput.focus(), 0);
  }

  private renderPalette(): void {
    const search = this.paletteInput.value;
    this.visiblePaletteActions = this.paletteActions
      .map((action) => ({ action, score: scoreAction(action, search) }))
      .filter((entry): entry is ScoredAction => Number.isFinite(entry.score))
      .sort((left, right) => right.score - left.score)
      .slice(0, 8)
      .map((entry) => entry.action);
    this.paletteIndex = Math.max(0, Math.min(this.paletteIndex, this.visiblePaletteActions.length - 1));
    this.paletteList.replaceChildren();

    if (!this.visiblePaletteActions.length) {
      const empty = document.createElement("p");
      empty.className = "demo-palette-empty";
      empty.textContent = "No command found. Try “table”, “export”, or “schema”.";
      this.paletteList.append(empty);
      this.paletteInput.removeAttribute("aria-activedescendant");
      return;
    }

    this.visiblePaletteActions.forEach((action, index) => {
      const option = document.createElement("div");
      option.id = `demo-palette-option-${index}`;
      option.dataset.paletteIndex = String(index);
      option.setAttribute("role", "option");
      option.setAttribute("aria-selected", String(index === this.paletteIndex));
      const icon = document.createElement("span");
      icon.className = "demo-command-icon";
      icon.setAttribute("aria-hidden", "true");
      icon.textContent = action.id.startsWith("table-") ? "▦" : action.id === "run" ? "▶" : "⌘";
      const copy = document.createElement("span");
      copy.className = "demo-command-copy";
      const label = document.createElement("strong");
      appendHighlighted(label, action.label, search);
      const description = document.createElement("small");
      description.textContent = action.description;
      copy.append(label, description);
      option.append(icon, copy);
      this.paletteList.append(option);
    });
    this.paletteInput.setAttribute("aria-activedescendant", `demo-palette-option-${this.paletteIndex}`);
  }

  private setPaletteIndex(index: number, scroll: boolean): void {
    if (!this.visiblePaletteActions.length) return;
    this.paletteIndex = Math.max(0, Math.min(index, this.visiblePaletteActions.length - 1));
    this.paletteList.querySelectorAll<HTMLElement>("[role='option']").forEach((option, optionIndex) => {
      option.setAttribute("aria-selected", String(optionIndex === this.paletteIndex));
    });
    const active = query<HTMLElement>(this.paletteList, `[data-palette-index="${this.paletteIndex}"]`);
    this.paletteInput.setAttribute("aria-activedescendant", active.id);
    if (scroll) active.scrollIntoView({ block: "nearest" });
  }

  private handlePaletteKeys(event: KeyboardEvent): void {
    if (event.key === "ArrowDown") {
      event.preventDefault();
      this.setPaletteIndex((this.paletteIndex + 1) % this.visiblePaletteActions.length, true);
    } else if (event.key === "ArrowUp") {
      event.preventDefault();
      this.setPaletteIndex(
        (this.paletteIndex - 1 + this.visiblePaletteActions.length) % this.visiblePaletteActions.length,
        true
      );
    } else if (event.key === "Enter") {
      event.preventDefault();
      void this.choosePaletteAction(this.paletteIndex);
    }
  }

  private async choosePaletteAction(index: number): Promise<void> {
    const action = this.visiblePaletteActions[index];
    if (!action) return;
    this.paletteDialog.close();
    await action.run();
  }

  private openFilter(opener: HTMLElement): void {
    if (!this.result.columns.length) {
      this.announce("Run a query before opening result filters.");
      return;
    }
    this.filterColumn.replaceChildren();
    this.result.columns.forEach((column) => {
      const option = document.createElement("option");
      option.value = column;
      option.textContent = column;
      option.selected = column === (this.filter?.column ?? this.result.columns[this.selectedColumn]);
      this.filterColumn.append(option);
    });
    this.filterOperator.value = this.filter?.operator ?? "contains";
    this.filterValue.value = this.filter?.value ?? "";
    this.focusReturn.set(this.filterDialog, opener);
    this.filterDialog.showModal();
    window.setTimeout(() => this.filterValue.focus(), 0);
  }

  private openHelp(opener: HTMLElement): void {
    if (this.helpDialog.open) return;
    this.focusReturn.set(this.helpDialog, opener);
    this.helpDialog.showModal();
    window.setTimeout(() => query<HTMLButtonElement>(this.helpDialog, "[data-demo-help-close]").focus(), 0);
  }

  private exportCsv(): void {
    if (!this.result.columns.length) {
      this.announce("Run a query before exporting CSV.");
      return;
    }
    const rows = this.visibleRows();
    const csv = [
      this.result.columns.map(csvValue).join(","),
      ...rows.map((row) => row.map(csvValue).join(","))
    ].join("\r\n");
    const blob = new Blob([csv], { type: "text/csv;charset=utf-8" });
    const url = URL.createObjectURL(blob);
    const anchor = document.createElement("a");
    const scope = this.selectedTable ?? "query";
    anchor.href = url;
    anchor.download = `dbterm-${scope}-demo.csv`;
    document.body.append(anchor);
    anchor.click();
    anchor.remove();
    window.setTimeout(() => URL.revokeObjectURL(url), 0);
    this.announce(`Exported ${rows.length} visible rows as CSV.`);
  }

  private updateTableCounts(): void {
    this.root.querySelectorAll<HTMLElement>("[data-demo-table-count]").forEach((count) => {
      const table = count.dataset.demoTableCount;
      count.textContent = table ? String(TABLE_COUNTS[table] ?? 0) : "0";
    });
  }

  private updateTableSelection(): void {
    this.root.querySelectorAll<HTMLButtonElement>("[data-demo-table]").forEach((button) => {
      const active = button.dataset.demoTable === this.selectedTable;
      button.setAttribute("aria-pressed", String(active));
    });
  }

  private setLoading(loading: boolean, message?: string): void {
    this.isLoading = loading;
    this.root.dataset.loading = String(loading);
    this.root.setAttribute("aria-busy", String(loading));
    this.runButton.disabled = loading;
    this.resetButton.disabled = loading;
    if (message) this.announce(message);
  }

  private setEngineState(state: "idle" | "loading" | "ready" | "error", label: string): void {
    const badge = query<HTMLElement>(this.root, "[data-demo-engine]");
    badge.dataset.state = state;
    query<HTMLElement>(badge, "[data-demo-engine-label]").textContent = label;
  }

  private announce(message: string): void {
    this.status.textContent = message;
  }
}

export const mountInteractiveDemo = (root: HTMLElement): void => {
  if (root.dataset.demoMounted === "true") return;
  root.dataset.demoMounted = "true";
  new DbtermDemo(root);
};
