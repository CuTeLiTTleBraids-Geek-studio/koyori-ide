import { beforeEach, describe, expect, it, vi } from "vitest";
import type {
  DatabaseColumn,
  DatabaseConnectionInfo,
  DatabaseQueryResult,
  DatabaseTable,
} from "@/types";
import {
  cancelDatabaseQuery,
  connectDatabase,
  connectSQLiteDatabase,
  databaseState,
  disconnectDatabase,
  resetDatabaseStore,
  runDatabaseQuery,
  selectDatabaseConnection,
  selectDatabaseSchema,
  selectDatabaseTable,
  setDatabaseBackend,
  type DatabaseBackend,
} from "./database";

const connection: DatabaseConnectionInfo = { id: "fixture", name: "Fixture", provider: "sqlite" };
const tables: DatabaseTable[] = [{ name: "users", type: "table" }];
const columns: DatabaseColumn[] = [
  { name: "id", dataType: "INTEGER", nullable: true, primaryKey: true, ordinal: 0 },
  { name: "name", dataType: "TEXT", nullable: false, primaryKey: false, ordinal: 1 },
];

function result(page = 0, hasMore = false): DatabaseQueryResult {
  return {
    requestId: `query-${page}`,
    columns: [
      { name: "id", databaseType: "INTEGER", nullable: true },
      { name: "name", databaseType: "TEXT", nullable: false },
    ],
    rows: [[page * 2 + 1, "Ada"], [page * 2 + 2, "Grace"]],
    page,
    pageSize: 2,
    hasMore,
    durationMs: 4,
  };
}

function createBackend(): DatabaseBackend {
  return {
    connect: vi.fn().mockResolvedValue(connection),
    listConnections: vi.fn().mockResolvedValue([connection]),
    disconnect: vi.fn().mockResolvedValue(undefined),
    listSchemas: vi.fn().mockResolvedValue([{ name: "main" }]),
    listTables: vi.fn().mockResolvedValue(tables),
    describeTable: vi.fn().mockResolvedValue(columns),
    queryPage: vi.fn().mockResolvedValue(result()),
    cancelQuery: vi.fn().mockResolvedValue(true),
  };
}

function deferred<T>() {
  let resolve!: (value: T) => void;
  const promise = new Promise<T>((done) => { resolve = done; });
  return { promise, resolve };
}

describe("database store", () => {
  beforeEach(() => resetDatabaseStore());

  it("connects SQLite without retaining its path and loads schema", async () => {
    const backend = createBackend();
    setDatabaseBackend(backend);

    expect(await connectSQLiteDatabase("C:/secret/data.db", "Local data")).toBe(true);

    expect(backend.connect).toHaveBeenCalledWith(expect.objectContaining({
      provider: "sqlite",
      databasePath: "C:/secret/data.db",
      name: "Local data",
    }));
    expect(databaseState.connections).toEqual([connection]);
    expect(JSON.stringify(databaseState.connections)).not.toContain("data.db");
    expect(databaseState.activeConnectionId).toBe("fixture");
    expect(databaseState.schemas).toEqual([{ name: "main" }]);
    expect(databaseState.activeSchema).toBe("main");
    expect(databaseState.tables).toEqual(tables);

    await selectDatabaseTable("users");
    expect(backend.describeTable).toHaveBeenCalledWith("fixture", "main", "users");
    expect(databaseState.columns).toEqual(columns);
    expect(databaseState.sql).toBe('SELECT * FROM "users"');
  });

  it("connects relational providers with an opaque credential config id", async () => {
    const backend = createBackend();
    backend.connect = vi.fn().mockResolvedValue({
      id: "warehouse",
      name: "Warehouse",
      provider: "postgres",
      defaultSchema: "analytics",
    });
    backend.listSchemas = vi.fn().mockResolvedValue([{ name: "analytics" }, { name: "public" }]);
    backend.listTables = vi.fn().mockResolvedValue([]);
    setDatabaseBackend(backend);

    expect(await connectDatabase({
      provider: "postgres",
      name: "Warehouse",
      credentialConfigId: "database-prod",
      defaultSchema: "analytics",
    })).toBe(true);

    expect(backend.connect).toHaveBeenCalledWith(expect.objectContaining({
      provider: "postgres",
      credentialConfigId: "database-prod",
      defaultSchema: "analytics",
    }));
    const payload = JSON.stringify(vi.mocked(backend.connect).mock.calls[0][0]);
    expect(payload).not.toContain("dsn");
    expect(payload).not.toContain("password");
    expect(databaseState.activeSchema).toBe("analytics");

    await selectDatabaseSchema("public");
    expect(backend.listTables).toHaveBeenLastCalledWith("warehouse", "public");
  });

  it("queries pages and preserves the backend pagination metadata", async () => {
    const backend = createBackend();
    backend.queryPage = vi.fn().mockImplementation(async (request) => result(request.page, request.page === 0));
    setDatabaseBackend(backend);
    databaseState.connections = [connection];
    databaseState.activeConnectionId = "fixture";
    databaseState.sql = "SELECT id, name FROM users ORDER BY id";
    databaseState.pageSize = 2;

    await runDatabaseQuery(0);
    expect(backend.queryPage).toHaveBeenCalledWith(expect.objectContaining({
      connectionId: "fixture",
      page: 0,
      pageSize: 2,
    }));
    expect(databaseState.result?.hasMore).toBe(true);

    await runDatabaseQuery(1);
    expect(databaseState.result?.page).toBe(1);
    expect(databaseState.result?.rows[0]).toEqual([3, "Ada"]);
  });

  it("cancels an active query and ignores its late result", async () => {
    const backend = createBackend();
    const pending = deferred<DatabaseQueryResult>();
    backend.queryPage = vi.fn(() => pending.promise);
    setDatabaseBackend(backend);
    databaseState.connections = [connection];
    databaseState.activeConnectionId = "fixture";
    databaseState.sql = "SELECT * FROM users";

    const running = runDatabaseQuery();
    await Promise.resolve();
    const requestId = databaseState.activeQueryId;
    expect(requestId).toBeTruthy();
    expect(await cancelDatabaseQuery()).toBe(true);
    expect(backend.cancelQuery).toHaveBeenCalledWith(requestId);
    pending.resolve(result());
    await running;

    expect(databaseState.result).toBeNull();
    expect(databaseState.queryRunning).toBe(false);
  });

  it("isolates late schema loads and disconnects the selected connection", async () => {
    const backend = createBackend();
    const slowTables = deferred<DatabaseTable[]>();
    backend.listSchemas = vi.fn().mockResolvedValue([{ name: "main" }]);
    backend.listTables = vi.fn((id) => (
      id === "slow" ? slowTables.promise : Promise.resolve([{ name: "current", type: "view" }])
    ));
    setDatabaseBackend(backend);
    databaseState.connections = [
      { id: "slow", name: "Slow", provider: "sqlite" },
      connection,
    ];

    const slow = selectDatabaseConnection("slow");
    await selectDatabaseConnection("fixture");
    slowTables.resolve([{ name: "stale", type: "table" }]);
    await slow;
    expect(databaseState.tables).toEqual([{ name: "current", type: "view" }]);

    expect(await disconnectDatabase("fixture")).toBe(true);
    expect(backend.disconnect).toHaveBeenCalledWith("fixture");
    expect(databaseState.activeConnectionId).toBe("slow");
  });
});
