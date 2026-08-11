import { mount } from "@vue/test-utils";
import { beforeEach, describe, expect, it, vi } from "vitest";

const mocks = vi.hoisted(() => ({
  state: {
    connections: [{ id: "fixture", name: "Fixture", provider: "sqlite" }],
    activeConnectionId: "fixture" as string | null,
    schemas: [{ name: "main" }],
    activeSchema: "main" as string | null,
    tables: [{ name: "users", type: "table" }],
    selectedTable: "users" as string | null,
    columns: [
      { name: "id", dataType: "INTEGER", nullable: true, primaryKey: true, ordinal: 0 },
      { name: "name", dataType: "TEXT", nullable: false, primaryKey: false, ordinal: 1 },
    ],
    sql: "SELECT * FROM users",
    result: {
      requestId: "query-1",
      columns: [
        { name: "id", databaseType: "INTEGER", nullable: true },
        { name: "name", databaseType: "TEXT", nullable: false },
      ],
      rows: [[1, "Ada"], [2, null]],
      page: 0,
      pageSize: 100,
      hasMore: true,
      durationMs: 3,
    } as Record<string, unknown> | null,
    pageSize: 100,
    connecting: false,
    schemaLoading: false,
    queryRunning: false,
    activeQueryId: null as string | null,
    error: null as string | null,
  },
  load: vi.fn(),
  connect: vi.fn().mockResolvedValue(true),
  disconnect: vi.fn(),
  selectConnection: vi.fn(),
  selectSchema: vi.fn(),
  selectTable: vi.fn(),
  run: vi.fn(),
  cancel: vi.fn(),
}));

vi.mock("@/stores/database", () => ({
  databaseState: mocks.state,
  loadDatabaseConnections: mocks.load,
  connectDatabase: mocks.connect,
  disconnectDatabase: mocks.disconnect,
  selectDatabaseConnection: mocks.selectConnection,
  selectDatabaseSchema: mocks.selectSchema,
  selectDatabaseTable: mocks.selectTable,
  runDatabaseQuery: mocks.run,
  cancelDatabaseQuery: mocks.cancel,
}));

vi.mock("@/lib/i18n", () => ({
  useI18n: () => ({
    t: (key: string, params?: Record<string, string | number>) => (
      params?.page ? `${key}:${params.page}` : key
    ),
  }),
}));

import DatabaseToolWindow from "./DatabaseToolWindow.vue";

function mountWindow() {
  return mount(DatabaseToolWindow, { global: { stubs: { "el-icon": true } } });
}

describe("DatabaseToolWindow", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    mocks.state.activeConnectionId = "fixture";
    mocks.state.connecting = false;
    mocks.state.schemaLoading = false;
    mocks.state.queryRunning = false;
    mocks.state.error = null;
    mocks.state.result = {
      requestId: "query-1",
      columns: [
        { name: "id", databaseType: "INTEGER", nullable: true },
        { name: "name", databaseType: "TEXT", nullable: false },
      ],
      rows: [[1, "Ada"], [2, null]],
      page: 0,
      pageSize: 100,
      hasMore: true,
      durationMs: 3,
    };
  });

  it("loads connections and renders schema, columns, and paged results", () => {
    const wrapper = mountWindow();

    expect(mocks.load).toHaveBeenCalledOnce();
    expect(wrapper.get('[data-table="users"]').text()).toContain("users");
    expect(wrapper.get('[data-test="database-result"]').text()).toContain("Ada");
    expect(wrapper.get('[data-test="database-result"]').text()).toContain("database.null");
    expect(wrapper.get('[data-test="database-next"]').attributes("disabled")).toBeUndefined();
    expect(wrapper.get('[data-test="database-previous"]').attributes("disabled")).toBeDefined();
  });

  it("connects a SQLite path and delegates schema actions", async () => {
    const wrapper = mountWindow();
    await wrapper.get('[data-test="database-name"]').setValue("Local");
    await wrapper.get('[data-test="database-path"]').setValue("C:/data/app.db");
    await wrapper.get('[data-test="database-connect"]').trigger("submit");
    await wrapper.get('[data-test="database-connection"]').setValue("fixture");
    await wrapper.get('[data-table="users"]').trigger("click");
    await wrapper.get('[data-test="database-disconnect"]').trigger("click");

    expect(mocks.connect).toHaveBeenCalledWith(expect.objectContaining({
      provider: "sqlite",
      databasePath: "C:/data/app.db",
      name: "Local",
    }));
    expect(mocks.selectConnection).toHaveBeenCalledWith("fixture");
    expect(mocks.selectTable).toHaveBeenCalledWith("users");
    expect(mocks.disconnect).toHaveBeenCalledWith("fixture");
  });

  it("connects PostgreSQL by credential config without a plaintext DSN field", async () => {
    const wrapper = mountWindow();
    await wrapper.get('[data-test="database-provider"]').setValue("postgres");
    await wrapper.get('[data-test="database-name"]').setValue("Warehouse");
    await wrapper.get('[data-test="database-credential-config"]').setValue("database-prod");
    await wrapper.get('[data-test="database-default-schema"]').setValue("analytics");
    expect(wrapper.find('[data-test="database-dsn"]').exists()).toBe(false);
    await wrapper.get("form").trigger("submit");

    expect(mocks.connect).toHaveBeenCalledWith(expect.objectContaining({
      provider: "postgres",
      name: "Warehouse",
      credentialConfigId: "database-prod",
      defaultSchema: "analytics",
    }));
  });

  it("runs the first page and navigates forward", async () => {
    const wrapper = mountWindow();
    await wrapper.get('[data-test="database-run"]').trigger("click");
    await wrapper.get('[data-test="database-next"]').trigger("click");

    expect(mocks.run).toHaveBeenNthCalledWith(1, 0);
    expect(mocks.run).toHaveBeenNthCalledWith(2, 1);
  });

  it("shows cancellation while a query is active", async () => {
    mocks.state.queryRunning = true;
    const wrapper = mountWindow();
    await wrapper.get('[data-test="database-cancel"]').trigger("click");
    expect(mocks.cancel).toHaveBeenCalledOnce();
  });
});
