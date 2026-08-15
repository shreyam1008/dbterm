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
  operator:
    | "equals"
    | "not-equals"
    | "greater"
    | "greater-equal"
    | "less"
    | "less-equal"
    | "contains"
    | "starts"
    | "is-null"
    | "is-not-null";
  value: string;
}

interface HistoryEntry {
  sql: string;
  table: string | null;
  filters: ResultFilter[];
  sortColumn: number;
  sortAscending: boolean;
  selectedRow: number;
  selectedColumn: number;
}

interface PaletteAction {
  id: string;
  kind: "ACTION" | "TABLE" | "RECENT SQL";
  label: string;
  description: string;
  keywords: string;
  shortcut?: string;
  run: () => void | Promise<void>;
}

interface ScoredAction {
  action: PaletteAction;
  score: number;
}

const DEFAULT_QUERY = "";

const TABLE_QUERIES: Record<string, string> = {
  users: "SELECT id, name, email, plan, status, created_at FROM users",
  orders:
    "SELECT id, user_id, product_id, status, quantity, total, created_at FROM orders",
  payments:
    "SELECT id, order_id, amount, method, status, paid_at FROM payments",
  products:
    "SELECT id, name, category, price, inventory FROM products"
};

const TABLE_COLUMNS: Record<string, string[]> = {
  users: ["id", "name", "email", "plan", "status", "created_at"],
  orders: ["id", "user_id", "product_id", "status", "quantity", "total", "created_at"],
  payments: ["id", "order_id", "amount", "method", "status", "paid_at"],
  products: ["id", "name", "category", "price", "inventory"]
};

const TABLE_DEFAULT_SORT: Record<string, string> = {
  users: "id ASC",
  orders: "created_at DESC",
  payments: "paid_at DESC",
  products: "id ASC"
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

const scoreAction = (action: PaletteAction, rawNeedle: string): number => {
  const needle = rawNeedle.trim().toLocaleLowerCase();
  if (!needle) return 1;
  const haystack = `${action.kind} ${action.label} ${action.description} ${action.keywords} ${action.shortcut ?? ""}`.toLocaleLowerCase();
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
  private readonly resultsTitle: HTMLElement;
  private readonly tablesTitle: HTMLElement;
  private readonly sortStatus: HTMLElement;
  private readonly panels: HTMLElement[];
  private readonly loadingOverlay: HTMLElement;
  private readonly loadingTitle: HTMLElement;
  private readonly loadingMessage: HTMLElement;
  private readonly paletteDialog: HTMLDialogElement;
  private readonly paletteInput: HTMLInputElement;
  private readonly paletteList: HTMLElement;
  private readonly paletteDetail: HTMLElement;
  private readonly filterDialog: HTMLDialogElement;
  private readonly filterForm: HTMLFormElement;
  private readonly filterColumn: HTMLSelectElement;
  private readonly filterOperator: HTMLSelectElement;
  private readonly filterValue: HTMLInputElement;
  private readonly filterTitle: HTMLElement;
  private readonly activeFilters: HTMLElement;
  private readonly helpDialog: HTMLDialogElement;
  private readonly detailDialog: HTMLDialogElement;
  private readonly detailList: HTMLElement;
  private sqlite: Sqlite3Static | null = null;
  private database: Database | null = null;
  private databasePromise: Promise<Database> | null = null;
  private result: QueryResult = { columns: [], rows: [], truncated: false, elapsedMs: 0 };
  private filters: ResultFilter[] = [];
  private selectedRow = 0;
  private selectedColumn = 0;
  private selectedRows = new Set<number>();
  private selectedTable: string | null = "users";
  private tableCursor = "users";
  private tableSearch = "";
  private pinnedTables = new Set<string>();
  private activeSql = "";
  private sortColumn = -1;
  private sortAscending = true;
  private focusedPanel: "tables" | "query" | "results" = "tables";
  private history: HistoryEntry[] = [];
  private copiedValue = "";
  private copiedWasNull = false;
  private requestId = 0;
  private loadingRevealTimer: number | null = null;
  private flashTimer: number | null = null;
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
    this.resultsTitle = query(root, "[data-demo-results-title]");
    this.tablesTitle = query(root, "[data-demo-tables-title]");
    this.sortStatus = query(root, "[data-demo-sort-status]");
    this.panels = Array.from(root.querySelectorAll<HTMLElement>("[data-demo-panel]"));
    this.loadingOverlay = query(root, "[data-demo-loading]");
    this.loadingTitle = query(root, "[data-demo-loading-title]");
    this.loadingMessage = query(root, "[data-demo-loading-message]");
    this.paletteDialog = query(root, "[data-demo-palette-dialog]");
    this.paletteInput = query(root, "[data-demo-palette-input]");
    this.paletteList = query(root, "[data-demo-palette-list]");
    this.paletteDetail = query(root, "[data-demo-palette-detail]");
    this.filterDialog = query(root, "[data-demo-filter-dialog]");
    this.filterForm = query(root, "[data-demo-filter-form]");
    this.filterColumn = query(root, "[data-demo-filter-column]");
    this.filterOperator = query(root, "[data-demo-filter-operator]");
    this.filterValue = query(root, "[data-demo-filter-value]");
    this.filterTitle = query(root, "[data-demo-filter-title]");
    this.activeFilters = query(root, "[data-demo-active-filters]");
    this.helpDialog = query(root, "[data-demo-help-dialog]");
    this.detailDialog = query(root, "[data-demo-detail-dialog]");
    this.detailList = query(root, "[data-demo-detail-list]");
    this.editor.value = DEFAULT_QUERY;
    this.paletteActions = this.createPaletteActions();
    this.bindEvents();
    this.updateTableCounts();
    this.renderEmpty();
    this.updateTableSelection();
    this.setPanelFocus("tables", false);
    this.armLazyBoot();
  }

  private bindEvents(): void {
    this.runButton.addEventListener("click", () => void this.runQuery());
    this.resetButton.addEventListener("click", () => void this.reset());

    this.root.querySelectorAll<HTMLButtonElement>("[data-demo-table]").forEach((button) => {
      button.addEventListener("click", () => {
        const table = button.dataset.demoTable;
        if (!table) return;
        this.tableCursor = table;
        this.clearTableSearch(false);
        this.setPanelFocus("tables", false);
        void this.openTable(table, true);
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
      if (event.key === "Enter" && !event.shiftKey && !event.altKey) {
        event.preventDefault();
        void this.runQuery();
      }
    });

    this.resultBody.addEventListener("click", (event) => {
      const cell = (event.target as Element).closest<HTMLButtonElement>("[data-demo-cell]");
      if (cell) {
        this.setPanelFocus("results", false);
        this.selectCell(cell, true);
      }
    });
    this.resultBody.addEventListener("keydown", (event) => this.handleCellArrows(event));

    this.root.addEventListener("keydown", (event) => this.handleRootShortcuts(event));
    window.addEventListener("keydown", (event) => this.handlePageShortcuts(event));
    this.root.addEventListener("focusin", (event) => {
      const panel = (event.target as Element | null)?.closest<HTMLElement>("[data-demo-panel]");
      const name = panel?.dataset.demoPanel;
      if (name === "tables" || name === "query" || name === "results") {
        this.setPanelFocus(name, false);
      }
    });

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
      void this.applyFilter({
        column: this.filterColumn.value,
        operator: this.filterOperator.value as ResultFilter["operator"],
        value: this.filterValue.value
      }, false);
      this.filterDialog.close();
    });
    query<HTMLButtonElement>(this.filterDialog, "[data-demo-filter-add]").addEventListener(
      "click",
      () => {
        void this.applyFilter({
          column: this.filterColumn.value,
          operator: this.filterOperator.value as ResultFilter["operator"],
          value: this.filterValue.value
        }, true);
        this.filterDialog.close();
      }
    );
    query<HTMLButtonElement>(this.filterDialog, "[data-demo-filter-clipboard]").addEventListener(
      "click",
      () => {
        void this.applyModalClipboardFilter();
        this.filterDialog.close();
      }
    );
    query<HTMLButtonElement>(this.filterDialog, "[data-demo-filter-remove]").addEventListener(
      "click",
      () => {
        void this.removeLastFilter();
        this.filterDialog.close();
      }
    );
    query<HTMLButtonElement>(this.filterDialog, "[data-demo-filter-reset]").addEventListener(
      "click",
      () => {
        void this.clearFilter();
        this.filterDialog.close();
      }
    );
    query<HTMLButtonElement>(this.filterDialog, "[data-demo-filter-close]").addEventListener(
      "click",
      () => this.filterDialog.close()
    );
    this.filterOperator.addEventListener("change", () => this.updateFilterValueState());

    query<HTMLButtonElement>(this.helpDialog, "[data-demo-help-close]").addEventListener(
      "click",
      () => this.helpDialog.close()
    );
    query<HTMLButtonElement>(this.detailDialog, "[data-demo-detail-close]").addEventListener(
      "click",
      () => this.detailDialog.close()
    );
    this.detailDialog.addEventListener("keydown", (event) => {
      if (event.key === "Enter") {
        event.preventDefault();
        this.detailDialog.close();
      }
    });

    this.root.querySelectorAll<HTMLDialogElement>("dialog").forEach((dialog) => {
      dialog.addEventListener("keydown", (event) => {
        if (event.key !== "Escape") return;
        event.preventDefault();
        dialog.close();
      });
      dialog.addEventListener("click", (event) => {
        if (event.target === dialog) dialog.close();
      });
      dialog.addEventListener("close", () => {
        const target = this.focusReturn.get(dialog);
        const active = document.activeElement;
        const focusStillBelongsToDialog =
          active === document.body || active === dialog || Boolean(active && dialog.contains(active));
        if (focusStillBelongsToDialog && target?.isConnected) target.focus();
      });
    });
  }

  private createPaletteActions(): PaletteAction[] {
    return [
      {
        id: "focus-tables",
        kind: "ACTION",
        label: "Focus Tables",
        description: "Move focus to the database object list so you can find and open a table.",
        keywords: "sidebar objects navigation browse",
        shortcut: "Alt+T",
        run: () => this.setPanelFocus("tables")
      },
      {
        id: "focus-query",
        kind: "ACTION",
        label: "Focus Query Editor",
        description: "Move focus to the SQL editor and keep the current query text intact.",
        keywords: "sql statement editor write",
        shortcut: "Alt+Q",
        run: () => this.setPanelFocus("query")
      },
      {
        id: "focus-results",
        kind: "ACTION",
        label: "Focus Results",
        description: "Move focus to the result grid for cell navigation, filtering, and row actions.",
        keywords: "data grid cells rows navigation",
        shortcut: "Alt+R",
        run: () => this.setPanelFocus("results")
      },
      {
        id: "run",
        kind: "ACTION",
        label: "Run Current SQL",
        description: "Execute the SQL currently in the Query editor against the sample SQLite database.",
        keywords: "execute statement editor",
        shortcut: "Enter",
        run: () => this.runQuery()
      },
      {
        id: "toggle-table-pin",
        kind: "ACTION",
        label: "Pin / Unpin Selected Table",
        description: "Move the highlighted table into or out of the pinned section for this database connection.",
        keywords: "favorite favourite top sidebar table",
        shortcut: "Space (Tables)",
        run: () => {
          this.setPanelFocus("tables");
          this.toggleTablePin();
        }
      },
      ...Object.keys(TABLE_QUERIES).map((table) => ({
        id: `table-${table}`,
        kind: "TABLE" as const,
        label: table,
        description: `Browse ${TABLE_COUNTS[table]} sample rows from ${table}.`,
        keywords: `database table select ${table}`,
        run: () => this.openTable(table, true)
      })),
      {
        id: "filter",
        kind: "ACTION",
        label: "Filter Selected Column",
        description: "Open the typed filter builder for the selected table column.",
        keywords: "where search operator contains starts null",
        shortcut: "/",
        run: () => this.openFilter(this.runButton)
      },
      {
        id: "filter-clipboard",
        kind: "ACTION",
        label: "Filter Column by Clipboard",
        description: "Apply equality on the selected column using the copied value.",
        keywords: "paste value cross table lookup",
        shortcut: "V",
        run: () => this.filterWithClipboard()
      },
      {
        id: "copy",
        kind: "ACTION",
        label: "Copy Selected Cell",
        description: "Copy the complete selected cell value to the clipboard.",
        keywords: "clipboard full raw value",
        shortcut: "C",
        run: () => this.copySelectedCell()
      },
      {
        id: "follow",
        kind: "ACTION",
        label: "Follow Selected Foreign Key",
        description: "Open the referenced row; Backspace returns to the prior result.",
        keywords: "relationship join reference navigation",
        shortcut: "F",
        run: () => this.followSelectedForeignKey()
      },
      {
        id: "sort",
        kind: "ACTION",
        label: "Sort by Selected Column",
        description: "Toggle ascending or descending sorting for the active result.",
        keywords: "order ascending descending",
        shortcut: "S",
        run: () => this.toggleSort()
      },
      {
        id: "detail",
        kind: "ACTION",
        label: "Open Selected Row Details",
        description: "Inspect every full value in the selected row vertically.",
        keywords: "inspect record full json",
        shortcut: "Enter",
        run: () => this.openRowDetail(this.runButton)
      },
      {
        id: "export",
        kind: "ACTION",
        label: "Export Results to CSV",
        description: "Download the currently visible result scope as CSV.",
        keywords: "csv download save alt e",
        shortcut: "Alt+E",
        run: () => this.exportCsv()
      },
      {
        id: "help",
        kind: "ACTION",
        label: "Open Help & SQL Cheatsheets",
        description: "Show dbterm keyboard workflows and the browser demo key map.",
        keywords: "commands shortcuts keys alt h",
        shortcut: "Alt+H",
        run: () => this.openHelp(this.runButton)
      },
      {
        id: "fullscreen",
        kind: "ACTION",
        label: "Toggle Fullscreen Results",
        description: "Expand the result grid to the full sample workspace or restore the layout.",
        keywords: "maximize expand data grid",
        shortcut: "Alt+F",
        run: () => this.toggleFullscreen()
      },
      {
        id: "clear-filters",
        kind: "ACTION",
        label: "Clear All Active Filters",
        description: "Remove every active table predicate and reload the table.",
        keywords: "reset where predicates",
        shortcut: "Esc",
        run: () => this.clearFilter()
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
    if (!("IntersectionObserver" in window)) {
      void this.openTable("users", false, false);
      return;
    }
    this.lazyObserver = new IntersectionObserver(
      (entries) => {
        if (!entries.some((entry) => entry.isIntersecting)) return;
        this.lazyObserver?.disconnect();
        this.lazyObserver = null;
        void this.openTable("users", false, false);
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
    const sql = this.editor.value.trim();
    if (!sql) {
      this.announce("Type a SQL query first.");
      this.setPanelFocus("query");
      return;
    }
    this.pushHistory();
    this.selectedTable = null;
    this.filters = [];
    this.sortColumn = -1;
    this.sortAscending = true;
    await this.runSql(sql, "Running SQL…", "Press Esc or Ctrl+C to cancel this query.");
    this.setPanelFocus("results");
  }

  private async runSql(sql: string, loadingTitle: string, loadingMessage: string): Promise<void> {
    this.lazyObserver?.disconnect();
    this.lazyObserver = null;
    if (this.isLoading) this.cancelQuery();
    const currentRequest = ++this.requestId;
    this.activeSql = sql;
    this.setLoading(true, loadingTitle, loadingMessage);

    try {
      const database = await this.ensureDatabase();
      if (currentRequest !== this.requestId) return;
      this.result = this.execute(sql, database);
      this.selectedRow = 0;
      this.selectedColumn = 0;
      this.selectedRows.clear();
      this.renderResult();
      const qualifier = this.result.truncated ? " (first 100)" : "";
      this.announce(
        `${this.result.rows.length} row${this.result.rows.length === 1 ? "" : "s"}${qualifier} in ${this.result.elapsedMs.toFixed(1)} ms.`
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
    this.setLoading(false);
    this.announce("Operation cancelled. Your workspace is unchanged.");
  }

  private async reset(): Promise<void> {
    this.cancelQuery();
    this.database?.close();
    this.database = null;
    this.databasePromise = null;
    this.sqlite = null;
    this.history = [];
    this.filters = [];
    this.selectedTable = "users";
    this.tableCursor = "users";
    this.tableSearch = "";
    this.pinnedTables.clear();
    this.root.querySelectorAll<HTMLButtonElement>("[data-demo-table]").forEach((button) => {
      button.dataset.pinned = "false";
      button.removeAttribute("aria-label");
    });
    this.renderTableOrder();
    this.activeSql = "";
    this.sortColumn = -1;
    this.sortAscending = true;
    this.selectedRows.clear();
    this.editor.value = DEFAULT_QUERY;
    this.renderEmpty("Rebuilding the private sample database…");
    await this.openTable("users", false);
    this.setPanelFocus("tables");
    this.announce("Sample database reset.");
  }

  private pushHistory(): void {
    const sql = this.activeSql.trim();
    const previous = this.history.at(-1);
    if (!sql || (previous?.sql === sql && previous.table === this.selectedTable)) return;
    this.history.push({
      sql,
      table: this.selectedTable,
      filters: this.filters.map((filter) => ({ ...filter })),
      sortColumn: this.sortColumn,
      sortAscending: this.sortAscending,
      selectedRow: this.selectedRow,
      selectedColumn: this.selectedColumn
    });
    if (this.history.length > 20) this.history.shift();
  }

  private async navigateBack(): Promise<void> {
    const previous = this.history.pop();
    if (!previous) {
      this.announce("No earlier table in this tour yet.");
      return;
    }
    this.selectedTable = previous.table;
    this.tableCursor = previous.table ?? this.tableCursor;
    this.filters = previous.filters.map((filter) => ({ ...filter }));
    this.sortColumn = previous.sortColumn;
    this.sortAscending = previous.sortAscending;
    this.updateTableSelection();
    await this.runSql(previous.sql, "Returning to the previous result…", "Press Esc to cancel navigation.");
    this.selectedRow = Math.min(previous.selectedRow, Math.max(0, this.result.rows.length - 1));
    this.selectedColumn = Math.min(previous.selectedColumn, Math.max(0, this.result.columns.length - 1));
    this.renderResult();
    this.setPanelFocus("results");
  }

  private async openTable(table: string, addHistory: boolean, focusResults = true): Promise<void> {
    if (!TABLE_QUERIES[table]) return;
    if (addHistory) this.pushHistory();
    this.selectedTable = table;
    this.tableCursor = table;
    this.filters = [];
    this.sortColumn = -1;
    this.sortAscending = true;
    this.updateTableSelection();
    await this.reloadTable(`Opening ${table}…`, false);
    if (focusResults) this.setPanelFocus("results");
  }

  private async followSelectedForeignKey(): Promise<void> {
    if (!this.selectedTable) {
      this.announce("Foreign-key navigation is available while browsing a table.");
      return;
    }
    const value = this.selectedValue();
    const column = this.result.columns[this.selectedColumn];
    const target = FK_TARGETS[column];
    if (!target || value === undefined || value === null) {
      this.announce("Select a user_id, order_id, or product_id cell to follow it.");
      return;
    }
    this.pushHistory();
    this.selectedTable = target.table;
    this.tableCursor = target.table;
    this.filters = [{ column: target.column, operator: "equals", value: String(value) }];
    this.sortColumn = -1;
    this.sortAscending = true;
    this.updateTableSelection();
    await this.reloadTable(`Following ${column} → ${target.table}.${target.column}…`);
    this.setPanelFocus("results");
  }

  private visibleRows(): DemoValue[][] {
    return this.result.rows;
  }

  private async applyFilter(filter: ResultFilter, addAnd = false): Promise<void> {
    if (!this.selectedTable || !TABLE_COLUMNS[this.selectedTable]?.includes(filter.column)) {
      this.announce("Column filters are available while browsing a table.");
      return;
    }
    if (addAnd) {
      this.filters.push(filter);
    } else {
      const existing = this.filters.findIndex((entry) => entry.column === filter.column);
      if (existing >= 0) this.filters[existing] = filter;
      else this.filters.push(filter);
    }
    this.selectedRow = 0;
    this.selectedColumn = Math.max(0, this.result.columns.indexOf(filter.column));
    await this.reloadTable(`Applying ${filter.column} filter…`);
  }

  private async clearFilter(): Promise<void> {
    if (!this.filters.length || !this.selectedTable) return;
    this.filters = [];
    this.selectedRow = 0;
    await this.reloadTable("Clearing filters…");
  }

  private async removeLastFilter(): Promise<void> {
    if (!this.filters.length || !this.selectedTable) return;
    this.filters.pop();
    await this.reloadTable("Removing the last filter…");
  }

  private async reloadTable(message: string, restoreDomFocus = true): Promise<void> {
    if (!this.selectedTable) return;
    const restoreFocus = this.focusedPanel;
    const sql = this.buildTableSql(this.selectedTable);
    await this.runSql(sql, message, "Press Esc to cancel opening this table.");
    if (restoreDomFocus) this.setPanelFocus(restoreFocus);
  }

  private buildTableSql(table: string): string {
    const base = TABLE_QUERIES[table];
    const columns = TABLE_COLUMNS[table];
    if (!base || !columns) throw new Error(`Unknown sample table: ${table}`);
    const predicates = this.filters
      .filter((filter) => columns.includes(filter.column))
      .map((filter) => this.filterSql(filter));
    let sql = base;
    if (predicates.length) sql += ` WHERE ${predicates.join(" AND ")}`;
    if (this.sortColumn >= 0 && columns[this.sortColumn]) {
      sql += ` ORDER BY "${columns[this.sortColumn]}" ${this.sortAscending ? "ASC" : "DESC"}`;
    } else {
      sql += ` ORDER BY ${TABLE_DEFAULT_SORT[table]}`;
    }
    return `${sql};`;
  }

  private filterSql(filter: ResultFilter): string {
    const column = `"${filter.column.replaceAll('"', '""')}"`;
    const escaped = filter.value.replaceAll("'", "''");
    const literal = `'${escaped}'`;
    switch (filter.operator) {
      case "not-equals": return `${column} != ${literal}`;
      case "greater": return `${column} > ${literal}`;
      case "greater-equal": return `${column} >= ${literal}`;
      case "less": return `${column} < ${literal}`;
      case "less-equal": return `${column} <= ${literal}`;
      case "contains": return `CAST(${column} AS TEXT) LIKE '%${this.escapeLike(escaped)}%' ESCAPE '\\'`;
      case "starts": return `CAST(${column} AS TEXT) LIKE '${this.escapeLike(escaped)}%' ESCAPE '\\'`;
      case "is-null": return `${column} IS NULL`;
      case "is-not-null": return `${column} IS NOT NULL`;
      default: return `${column} = ${literal}`;
    }
  }

  private escapeLike(value: string): string {
    return value.replaceAll("\\", "\\\\").replaceAll("%", "\\%").replaceAll("_", "\\_");
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
    this.copiedWasNull = value === null;
    try {
      await navigator.clipboard.writeText(text);
      this.announce(`Copied ${text} to the clipboard.`);
    } catch {
      this.announce(`Selected value: ${text}. Clipboard permission was unavailable.`);
    }
  }

  private async filterWithClipboard(): Promise<void> {
    if (!this.selectedTable) {
      this.announce("Clipboard filtering is available while browsing a table.");
      return;
    }
    const column = this.result.columns[this.selectedColumn];
    if (!column) {
      this.announce("Select a result cell before filtering from the clipboard.");
      return;
    }
    const clipboard = await this.readClipboard();
    if (!clipboard && !this.copiedWasNull) {
      this.announce("The clipboard is empty. Press C on a selected cell first.");
      return;
    }
    const operator: ResultFilter["operator"] = this.copiedWasNull && clipboard === "NULL" ? "is-null" : "equals";
    await this.applyFilter({ column, operator, value: clipboard }, false);
  }

  private async applyModalClipboardFilter(): Promise<void> {
    const clipboard = await this.readClipboard();
    const selectedOperator = this.filterOperator.value as ResultFilter["operator"];
    const operator = this.copiedWasNull && clipboard === "NULL" && selectedOperator === "equals"
      ? "is-null"
      : selectedOperator;
    await this.applyFilter({ column: this.filterColumn.value, operator, value: clipboard }, false);
  }

  private async readClipboard(): Promise<string> {
    let clipboard = this.copiedValue;
    try {
      const liveClipboard = await navigator.clipboard.readText();
      if (liveClipboard && liveClipboard !== this.copiedValue) {
        clipboard = liveClipboard;
        this.copiedWasNull = false;
      }
    } catch {
      // Browser permission is optional; the last dbterm C-copy remains available.
    }
    return clipboard;
  }

  private updateFilterValueState(): void {
    const operator = this.filterOperator.value as ResultFilter["operator"];
    const needsValue = operator !== "is-null" && operator !== "is-not-null";
    this.filterValue.disabled = !needsValue;
    this.filterValue.placeholder = needsValue ? "type a comparison value" : "not used for NULL operators";
  }

  private renderResult(): void {
    const rows = this.visibleRows();
    delete this.emptyState.dataset.state;
    this.resultHead.replaceChildren();
    this.resultBody.replaceChildren();
    this.emptyState.hidden = true;
    this.resultTable.hidden = false;

    const headRow = document.createElement("tr");
    this.result.columns.forEach((column, columnIndex) => {
      const header = document.createElement("th");
      header.scope = "col";
      const indicator = columnIndex === this.sortColumn ? (this.sortAscending ? " ▲" : " ▼") : "";
      header.textContent = `${column}${indicator}`;
      headRow.append(header);
    });
    this.resultHead.append(headRow);

    rows.forEach((row, rowIndex) => {
      const tableRow = document.createElement("tr");
      tableRow.dataset.rowSelected = String(this.selectedRows.has(rowIndex));
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
        if (typeof value === "number" || typeof value === "bigint") button.classList.add("is-number");
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

    this.rowCount.textContent = `${rows.length}${this.result.truncated && !this.filters.length ? "+" : ""} rows`;
    this.duration.textContent = `${this.result.elapsedMs.toFixed(1)} ms`;
    const title = this.selectedTable
      ? `${this.selectedTable.charAt(0).toUpperCase()}${this.selectedTable.slice(1)}`
      : "Query Results";
    this.resultsTitle.textContent = `${title} — ${rows.length} row${rows.length === 1 ? "" : "s"} in ${this.result.elapsedMs.toFixed(1)}ms`;
    const sortName = this.result.columns[this.sortColumn];
    this.sortStatus.textContent = sortName ? `sort: ${sortName} ${this.sortAscending ? "▲" : "▼"}` : "sort: none";
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
    this.resultsTitle.textContent = "Results";
    this.sortStatus.textContent = "sort: none";
    this.filterChip.hidden = true;
  }

  private renderError(message: string): void {
    this.result = { columns: [], rows: [], truncated: false, elapsedMs: 0 };
    this.selectedRow = 0;
    this.selectedColumn = 0;
    this.selectedRows.clear();
    this.resultHead.replaceChildren();
    this.resultBody.replaceChildren();
    this.resultTable.hidden = true;
    this.emptyState.hidden = false;
    this.emptyState.dataset.state = "error";
    query<HTMLElement>(this.emptyState, "[data-demo-empty-title]").textContent = "SQLite returned an error";
    query<HTMLElement>(this.emptyState, "[data-demo-empty-message]").textContent = message;
    this.rowCount.textContent = "0 rows";
    this.duration.textContent = "— ms";
    this.resultsTitle.textContent = "Results — error";
    this.filterChip.hidden = true;
    this.announce(`Query error: ${message}`);
  }

  private renderFilterChip(): void {
    const text = query<HTMLElement>(this.filterChip, "[data-demo-filter-text]");
    if (!this.filters.length) {
      this.filterChip.hidden = true;
      this.activeFilters.textContent = "Active filters: none";
      return;
    }
    const operatorLabels: Record<ResultFilter["operator"], string> = {
      contains: "contains",
      equals: "=",
      "not-equals": "!=",
      starts: "starts-with",
      greater: ">",
      "greater-equal": ">=",
      less: "<",
      "less-equal": "<=",
      "is-null": "IS NULL",
      "is-not-null": "IS NOT NULL"
    };
    const summary = this.filters.map((filter) => {
      const needsValue = filter.operator !== "is-null" && filter.operator !== "is-not-null";
      return `${filter.column} ${operatorLabels[filter.operator]}${needsValue ? ` “${filter.value}”` : ""}`;
    }).join(" AND ");
    text.textContent = `filter: ${summary}`;
    this.activeFilters.textContent = `Active filters: ${summary}`;
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

  private handlePageShortcuts(event: KeyboardEvent): void {
    if (event.defaultPrevented) return;

    const key = event.key.toLocaleLowerCase();
    const opensPalette = (event.ctrlKey || event.metaKey) && !event.altKey && key === "p";
    const focusesPanel = event.altKey && (key === "t" || key === "q" || key === "r");
    const opensHelp = event.altKey && key === "h";
    const exportsResult = event.altKey && key === "e";
    const togglesFullscreen = event.altKey && key === "f";
    if (!opensPalette && !focusesPanel && !opensHelp && !exportsResult && !togglesFullscreen) return;

    event.preventDefault();
    if (this.filterDialog.open || this.helpDialog.open || this.detailDialog.open || this.isLoading) return;
    this.revealDemo();

    if (opensPalette) {
      this.openPalette(this.root);
    } else if (focusesPanel) {
      this.setPanelFocus(key === "t" ? "tables" : key === "q" ? "query" : "results");
    } else if (opensHelp) {
      this.openHelp(this.root);
    } else if (exportsResult) {
      this.exportCsv();
    } else {
      this.toggleFullscreen();
    }
  }

  private revealDemo(): void {
    const page = document.documentElement;
    const previousBehavior = page.style.scrollBehavior;
    page.style.scrollBehavior = "auto";
    this.root.scrollIntoView({ behavior: "auto", block: "center" });
    page.style.scrollBehavior = previousBehavior;
  }

  private handleRootShortcuts(event: KeyboardEvent): void {
    if (!this.root.contains(document.activeElement)) return;
    const key = event.key.toLocaleLowerCase();
    const target = event.target instanceof Element ? event.target : null;
    const insideTerminal = Boolean(target?.closest("[data-demo-terminal]"));

    if ((insideTerminal || this.paletteDialog.open) && (event.ctrlKey || event.metaKey) && key === "p") {
      event.preventDefault();
      if (this.paletteDialog.open) this.paletteDialog.close();
      else this.openPalette(document.activeElement as HTMLElement);
      return;
    }
    if (insideTerminal && (event.ctrlKey || event.metaKey) && key === "c" && this.isLoading) {
      event.preventDefault();
      this.cancelQuery();
      return;
    }
    if ((insideTerminal || this.helpDialog.open) && event.altKey && key === "h") {
      event.preventDefault();
      if (this.helpDialog.open) this.helpDialog.close();
      else this.openHelp(document.activeElement as HTMLElement);
      return;
    }
    if (!insideTerminal) return;
    if (this.isLoading && event.key === "Escape") {
      event.preventDefault();
      this.cancelQuery();
      return;
    }
    if (this.paletteDialog.open || this.filterDialog.open || this.helpDialog.open || this.detailDialog.open) return;
    if (event.altKey && (key === "t" || key === "q" || key === "r")) {
      event.preventDefault();
      this.setPanelFocus(key === "t" ? "tables" : key === "q" ? "query" : "results");
      return;
    }
    if (event.altKey && key === "e") {
      event.preventDefault();
      this.exportCsv();
      return;
    }
    if (event.altKey && key === "f") {
      event.preventDefault();
      this.toggleFullscreen();
      return;
    }
    if (event.key === "Tab") {
      event.preventDefault();
      this.cyclePanel(event.shiftKey ? -1 : 1);
      return;
    }

    if (this.focusedPanel === "tables") {
      if (event.key === " " && !this.tableSearch) {
        event.preventDefault();
        this.toggleTablePin();
      } else if (event.key === "ArrowDown" || event.key === "ArrowUp") {
        event.preventDefault();
        this.moveTableCursor(event.key === "ArrowDown" ? 1 : -1);
      } else if (event.key === "Enter") {
        event.preventDefault();
        const table = this.tableCursor;
        this.clearTableSearch(false);
        void this.openTable(table, true);
      } else if (event.key === "Backspace") {
        if (!this.tableSearch) return;
        event.preventDefault();
        this.tableSearch = Array.from(this.tableSearch).slice(0, -1).join("");
        this.applyTableSearch();
      } else if (event.key === "Escape") {
        event.preventDefault();
        if (this.tableSearch) this.clearTableSearch();
        else this.releaseDemoFocus();
      } else if (!event.ctrlKey && !event.altKey && !event.metaKey && event.key.length === 1 && !isTypingTarget(event.target)) {
        event.preventDefault();
        this.tableSearch += event.key;
        this.applyTableSearch();
      }
      return;
    }

    if (this.focusedPanel === "query") {
      if (event.key === "Escape") {
        event.preventDefault();
        this.setPanelFocus("tables");
      }
      return;
    }

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
    } else if (key === "s" && !event.ctrlKey && !event.metaKey && !event.altKey) {
      event.preventDefault();
      void this.toggleSort();
    } else if (event.key === " ") {
      event.preventDefault();
      this.toggleRowSelection();
    } else if (event.key === "Enter") {
      event.preventDefault();
      this.openRowDetail(document.activeElement as HTMLElement);
    } else if (event.key === "Backspace") {
      event.preventDefault();
      void this.navigateBack();
    } else if (event.key === "Escape") {
      event.preventDefault();
      if (this.filters.length) void this.clearFilter();
      else this.setPanelFocus("tables");
    }
  }

  private setPanelFocus(panel: "tables" | "query" | "results", focus = true): void {
    this.focusedPanel = panel;
    this.root.querySelector<HTMLElement>("[data-demo-terminal]")!.dataset.focus = panel;
    this.panels.forEach((element) => {
      element.dataset.active = String(element.dataset.demoPanel === panel);
    });
    this.updateContextStatus();
    if (!focus) return;
    if (panel === "query") {
      this.editor.focus();
      return;
    }
    if (panel === "results") {
      const cell = this.resultBody.querySelector<HTMLButtonElement>("[data-demo-cell][data-selected='true']")
        ?? this.resultBody.querySelector<HTMLButtonElement>("[data-demo-cell]");
      if (cell) this.selectCell(cell, true);
      else this.panels.find((item) => item.dataset.demoPanel === "results")?.focus();
      return;
    }
    const table = this.root.querySelector<HTMLButtonElement>(`[data-demo-table="${this.tableCursor}"]`);
    table?.focus();
  }

  private cyclePanel(delta: number): void {
    const order: Array<"tables" | "query" | "results"> = ["tables", "query", "results"];
    const current = order.indexOf(this.focusedPanel);
    this.setPanelFocus(order[(current + delta + order.length) % order.length]);
  }

  private releaseDemoFocus(): void {
    const exit = this.root.querySelector<HTMLAnchorElement>(".demo-aftercare a");
    exit?.focus();
    this.announce("Demo focus released.");
  }

  private moveTableCursor(delta: number): void {
    const names = this.orderedTableNames();
    const current = Math.max(0, names.indexOf(this.tableCursor));
    this.tableCursor = names[(current + delta + names.length) % names.length];
    this.updateTableSelection();
    this.root.querySelector<HTMLButtonElement>(`[data-demo-table="${this.tableCursor}"]`)?.focus();
  }

  private applyTableSearch(): void {
    const needle = this.tableSearch.toLocaleLowerCase();
    const names = this.orderedTableNames();
    const match = names.find((name) => name.toLocaleLowerCase().includes(needle));
    if (match) this.tableCursor = match;
    this.root.querySelectorAll<HTMLButtonElement>("[data-demo-table]").forEach((button) => {
      const name = button.dataset.demoTable ?? "";
      const label = query<HTMLElement>(button, "[data-demo-table-label]");
      label.replaceChildren();
      const index = needle ? name.toLocaleLowerCase().indexOf(needle) : -1;
      if (index < 0) label.textContent = name;
      else {
        label.append(document.createTextNode(name.slice(0, index)));
        const mark = document.createElement("mark");
        mark.textContent = name.slice(index, index + this.tableSearch.length);
        label.append(mark, document.createTextNode(name.slice(index + this.tableSearch.length)));
      }
    });
    this.tablesTitle.textContent = `Tables (4)${this.tableSearch ? ` find: ${this.tableSearch}` : ""}`;
    this.updateTableSelection();
  }

  private clearTableSearch(announce = true): void {
    this.tableSearch = "";
    this.applyTableSearch();
    if (announce) this.announce("Table search cleared.");
  }

  private toggleTablePin(): void {
    const table = this.tableCursor;
    const button = this.root.querySelector<HTMLButtonElement>(`[data-demo-table="${table}"]`);
    if (!button) return;
    const pinned = !this.pinnedTables.has(table);
    if (pinned) this.pinnedTables.add(table);
    else this.pinnedTables.delete(table);
    button.dataset.pinned = String(pinned);
    if (pinned) button.setAttribute("aria-label", `Pinned table ${table}`);
    else button.removeAttribute("aria-label");
    this.renderTableOrder();
    this.announce(`${pinned ? "Pinned" : "Unpinned"} ${table}.`);
  }

  private orderedTableNames(): string[] {
    const names = Object.keys(TABLE_QUERIES);
    return [
      ...Array.from(this.pinnedTables).filter((table) => names.includes(table)),
      ...names.filter((table) => !this.pinnedTables.has(table))
    ];
  }

  private renderTableOrder(): void {
    const list = query<HTMLElement>(this.root, ".demo-table-list");
    this.orderedTableNames().forEach((table) => {
      const button = this.root.querySelector<HTMLButtonElement>(`[data-demo-table="${table}"]`);
      if (button) list.append(button);
    });
  }

  private async toggleSort(): Promise<void> {
    const column = this.result.columns[this.selectedColumn];
    if (!column) {
      this.announce("Select a result column before sorting.");
      return;
    }
    if (this.sortColumn === this.selectedColumn) this.sortAscending = !this.sortAscending;
    else {
      this.sortColumn = this.selectedColumn;
      this.sortAscending = true;
    }
    if (this.selectedTable) {
      await this.reloadTable(`Sorting ${column} ${this.sortAscending ? "ascending" : "descending"}…`);
      return;
    }
    const direction = this.sortAscending ? 1 : -1;
    const index = this.sortColumn;
    this.result.rows.sort((left, right) => {
      const a = left[index];
      const b = right[index];
      if (a === b) return 0;
      if (a === null) return -direction;
      if (b === null) return direction;
      return String(a).localeCompare(String(b), undefined, { numeric: true }) * direction;
    });
    this.renderResult();
    this.announce(`Sorted ${column} ${this.sortAscending ? "ascending" : "descending"}.`);
  }

  private toggleRowSelection(): void {
    if (!this.result.rows[this.selectedRow]) return;
    if (this.selectedRows.has(this.selectedRow)) this.selectedRows.delete(this.selectedRow);
    else this.selectedRows.add(this.selectedRow);
    const row = this.resultBody.querySelector<HTMLTableRowElement>(`tr:nth-child(${this.selectedRow + 1})`);
    if (row) row.dataset.rowSelected = String(this.selectedRows.has(this.selectedRow));
    this.announce(`${this.selectedRows.size} row${this.selectedRows.size === 1 ? "" : "s"} selected.`);
  }

  private openRowDetail(opener: HTMLElement): void {
    const row = this.result.rows[this.selectedRow];
    if (!row || !this.result.columns.length) {
      this.announce("Select a result row first.");
      return;
    }
    this.detailList.replaceChildren();
    this.result.columns.forEach((column, index) => {
      const term = document.createElement("dt");
      term.textContent = column;
      const detail = document.createElement("dd");
      detail.textContent = printable(row[index]);
      this.detailList.append(term, detail);
    });
    this.focusReturn.set(this.detailDialog, opener);
    this.detailDialog.showModal();
  }

  private toggleFullscreen(): void {
    const fullscreen = this.root.dataset.fullscreen !== "true";
    this.root.dataset.fullscreen = String(fullscreen);
    this.announce(fullscreen ? "Results expanded." : "Workspace restored.");
    if (fullscreen) this.setPanelFocus("results");
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
      .slice(0, 14)
      .map((entry) => entry.action);
    this.paletteIndex = Math.max(0, Math.min(this.paletteIndex, this.visiblePaletteActions.length - 1));
    this.paletteList.replaceChildren();

    if (!this.visiblePaletteActions.length) {
      const empty = document.createElement("p");
      empty.className = "demo-palette-empty";
      empty.textContent = "No command found. Try “table”, “export”, or “schema”.";
      this.paletteList.append(empty);
      this.paletteInput.removeAttribute("aria-activedescendant");
      this.paletteDetail.textContent = "No matching command or object. Try a shorter search.";
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
      icon.textContent = action.kind;
      const copy = document.createElement("span");
      copy.className = "demo-command-copy";
      const label = document.createElement("strong");
      appendHighlighted(label, action.label, search);
      const description = document.createElement("small");
      description.textContent = action.shortcut ?? "";
      copy.append(label, description);
      option.append(icon, copy);
      this.paletteList.append(option);
    });
    this.paletteInput.setAttribute("aria-activedescendant", `demo-palette-option-${this.paletteIndex}`);
    this.updatePaletteDetail();
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
    this.updatePaletteDetail();
  }

  private updatePaletteDetail(): void {
    const action = this.visiblePaletteActions[this.paletteIndex];
    this.paletteDetail.replaceChildren();
    if (!action) {
      this.paletteDetail.textContent = "No matching command or object.";
      return;
    }
    const title = document.createElement("strong");
    title.textContent = `${action.kind}  ${action.label}`;
    const description = document.createElement("span");
    description.textContent = action.description;
    this.paletteDetail.append(title, description);
    if (action.shortcut) {
      const shortcut = document.createElement("span");
      shortcut.append("Shortcut: ");
      const key = document.createElement("kbd");
      key.textContent = action.shortcut;
      shortcut.append(key);
      this.paletteDetail.append(shortcut);
    }
  }

  private handlePaletteKeys(event: KeyboardEvent): void {
    if (!this.visiblePaletteActions.length) return;
    if (event.key === "ArrowDown") {
      event.preventDefault();
      this.setPaletteIndex(this.paletteIndex + 1, true);
    } else if (event.key === "ArrowUp") {
      event.preventDefault();
      this.setPaletteIndex(this.paletteIndex - 1, true);
    } else if (event.key === "Enter") {
      event.preventDefault();
      void this.choosePaletteAction(this.paletteIndex);
    }
  }

  private async choosePaletteAction(index: number): Promise<void> {
    const action = this.visiblePaletteActions[index];
    if (!action) return;
    const closed = new Promise<void>((resolve) => {
      this.paletteDialog.addEventListener("close", () => resolve(), { once: true });
    });
    this.paletteDialog.close();
    await closed;
    await action.run();
  }

  private openFilter(opener: HTMLElement): void {
    if (!this.selectedTable || !this.result.columns.length) {
      this.announce("Column filters are available while browsing a table.");
      return;
    }
    const column = this.result.columns[this.selectedColumn];
    if (!column) return;
    this.filterColumn.replaceChildren();
    const option = document.createElement("option");
    option.value = column;
    option.textContent = column;
    option.selected = true;
    this.filterColumn.append(option);
    const active = [...this.filters].reverse().find((filter) => filter.column === column);
    this.filterOperator.value = active?.operator ?? "equals";
    this.filterValue.value = active?.value ?? "";
    this.filterTitle.textContent = `📊 Filters: ${this.selectedTable}.${column}`;
    this.renderFilterChip();
    this.updateFilterValueState();
    this.focusReturn.set(this.filterDialog, opener);
    this.filterDialog.showModal();
    window.setTimeout(() => {
      if (this.filterValue.disabled) this.filterOperator.focus();
      else this.filterValue.focus();
    }, 0);
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
      const active = button.dataset.demoTable === this.tableCursor;
      button.setAttribute("aria-selected", String(active));
      button.tabIndex = active ? 0 : -1;
    });
  }

  private setLoading(loading: boolean, title?: string, message?: string): void {
    this.isLoading = loading;
    this.root.dataset.loading = String(loading);
    this.root.setAttribute("aria-busy", String(loading));
    this.runButton.disabled = loading;
    this.resetButton.disabled = loading;
    if (this.loadingRevealTimer !== null) window.clearTimeout(this.loadingRevealTimer);
    this.loadingRevealTimer = null;
    if (loading) {
      this.loadingTitle.textContent = title ?? "Loading…";
      this.loadingMessage.textContent = message ?? "Press Esc to cancel.";
      this.loadingRevealTimer = window.setTimeout(() => {
        this.loadingOverlay.hidden = false;
        this.loadingOverlay.setAttribute("aria-hidden", "false");
      }, 90);
      if (title) this.status.textContent = title;
      return;
    }
    this.loadingOverlay.hidden = true;
    this.loadingOverlay.setAttribute("aria-hidden", "true");
  }

  private setEngineState(state: "idle" | "loading" | "ready" | "error", label: string): void {
    const badge = query<HTMLElement>(this.root, "[data-demo-engine]");
    badge.dataset.state = state;
    query<HTMLElement>(badge, "[data-demo-engine-label]").textContent = label;
  }

  private announce(message: string): void {
    if (this.flashTimer !== null) window.clearTimeout(this.flashTimer);
    this.status.textContent = message;
    this.flashTimer = window.setTimeout(() => this.updateContextStatus(), 2400);
  }

  private updateContextStatus(): void {
    if (this.isLoading) return;
    if (this.focusedPanel === "query") {
      this.status.textContent = "Enter Run ▶ · Shift+Enter Newline · Esc Tables";
      return;
    }
    if (this.focusedPanel === "results") {
      this.status.textContent = this.filters.length
        ? "Esc Clear filter · / Change · C Copy · Alt+E CSV"
        : "C Copy · / Filter · V Clipboard · F Follow FK · Alt+E CSV";
      return;
    }
    this.status.textContent = this.tableSearch
      ? `find: ${this.tableSearch} · Enter open · Backspace edit · Esc clear`
      : "Space Pin/unpin · type to find · Enter open · Esc release";
  }
}

export const mountInteractiveDemo = (root: HTMLElement): void => {
  if (root.dataset.demoMounted === "true") return;
  root.dataset.demoMounted = "true";
  new DbtermDemo(root);
};
