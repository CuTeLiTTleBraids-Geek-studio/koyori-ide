// Koyori IDE 模块 · Database；交互服务：数据库（DatabaseService）。
// 喵，这是 Koyori IDE 的 Database 模块（前端实现）~
import { computed, reactive } from "vue";
import { databaseService } from "@/api/services";
import { errorMessage } from "@/lib/errors";
import type {
  DatabaseColumn,
  DatabaseConnectionConfig,
  DatabaseConnectionInfo,
  DatabaseQueryRequest,
  DatabaseQueryResult,
  DatabaseSchema,
  DatabaseTable,
} from "@/types";

export type DatabaseProviderName = "sqlite" | "postgres" | "mysql";

export interface DatabaseConnectionInput {
  provider: DatabaseProviderName;
  name: string;
  databasePath?: string;
  credentialConfigId?: string;
  defaultSchema?: string;
}

export interface DatabaseBackend {
  connect(config: DatabaseConnectionConfig): Promise<DatabaseConnectionInfo>;
  listConnections(): Promise<DatabaseConnectionInfo[]>;
  disconnect(connectionId: string): Promise<void>;
  listSchemas(connectionId: string): Promise<DatabaseSchema[]>;
  listTables(connectionId: string, schema: string): Promise<DatabaseTable[]>;
  describeTable(connectionId: string, schema: string, table: string): Promise<DatabaseColumn[]>;
  queryPage(request: DatabaseQueryRequest): Promise<DatabaseQueryResult>;
  cancelQuery(requestId: string): Promise<boolean>;
}

interface DatabaseState {
  connections: DatabaseConnectionInfo[];
  activeConnectionId: string | null;
  schemas: DatabaseSchema[];
  activeSchema: string | null;
  tables: DatabaseTable[];
  selectedTable: string | null;
  columns: DatabaseColumn[];
  sql: string;
  result: DatabaseQueryResult | null;
  pageSize: number;
  connecting: boolean;
  schemaLoading: boolean;
  queryRunning: boolean;
  activeQueryId: string | null;
  error: string | null;
}

export const databaseState = reactive<DatabaseState>({
  connections: [],
  activeConnectionId: null,
  schemas: [],
  activeSchema: null,
  tables: [],
  selectedTable: null,
  columns: [],
  sql: "SELECT 1",
  result: null,
  pageSize: 100,
  connecting: false,
  schemaLoading: false,
  queryRunning: false,
  activeQueryId: null,
  error: null,
});

export const activeDatabaseConnection = computed<DatabaseConnectionInfo | null>(() => (
  databaseState.connections.find((connection) => connection.id === databaseState.activeConnectionId) ?? null
));

let backend: DatabaseBackend = databaseService;
let connectionSequence = 0;
let schemaGeneration = 0;
let queryGeneration = 0;

export function setDatabaseBackend(value: DatabaseBackend | null): void {
  backend = value ?? databaseService;
}

function createID(prefix: string): string {
  connectionSequence += 1;
  if (typeof crypto !== "undefined" && typeof crypto.randomUUID === "function") {
    return `${prefix}-${crypto.randomUUID()}`;
  }
  return `${prefix}-${Date.now()}-${connectionSequence}`;
}

function clearSchema(): void {
  databaseState.schemas = [];
  databaseState.activeSchema = null;
  databaseState.tables = [];
  databaseState.selectedTable = null;
  databaseState.columns = [];
  databaseState.result = null;
}

export async function loadDatabaseConnections(): Promise<void> {
  const generation = ++schemaGeneration;
  databaseState.schemaLoading = true;
  databaseState.error = null;
  try {
    const connections = await backend.listConnections();
    if (generation !== schemaGeneration) return;
    databaseState.connections = connections ?? [];
    if (!databaseState.connections.some(({ id }) => id === databaseState.activeConnectionId)) {
      databaseState.activeConnectionId = null;
      clearSchema();
    }
  } catch (error: unknown) {
    if (generation === schemaGeneration) databaseState.error = errorMessage(error);
  } finally {
    if (generation === schemaGeneration) databaseState.schemaLoading = false;
  }
}

export async function connectDatabase(input: DatabaseConnectionInput): Promise<boolean> {
  const databasePath = input.databasePath?.trim() ?? "";
  const credentialConfigId = input.credentialConfigId?.trim() ?? "";
  if (input.provider === "sqlite" && !databasePath) {
    databaseState.error = "SQLite database path is required";
    return false;
  }
  if (input.provider !== "sqlite" && !credentialConfigId) {
    databaseState.error = "Database credential config id is required";
    return false;
  }
  databaseState.connecting = true;
  databaseState.error = null;
  try {
    const info = await backend.connect({
      id: createID(input.provider),
      name: input.name.trim(),
      provider: input.provider,
      databasePath: databasePath || undefined,
      credentialConfigId: credentialConfigId || undefined,
      defaultSchema: input.defaultSchema?.trim() || undefined,
    });
    databaseState.connections = [
      ...databaseState.connections.filter(({ id }) => id !== info.id),
      info,
    ];
    await selectDatabaseConnection(info.id);
    return true;
  } catch (error: unknown) {
    databaseState.error = errorMessage(error);
    return false;
  } finally {
    databaseState.connecting = false;
  }
}

export function connectSQLiteDatabase(databasePath: string, name: string): Promise<boolean> {
  return connectDatabase({ provider: "sqlite", databasePath, name });
}

export async function selectDatabaseConnection(connectionId: string): Promise<void> {
  const generation = ++schemaGeneration;
  queryGeneration += 1;
  const previousQueryId = databaseState.activeQueryId;
  databaseState.activeQueryId = null;
  databaseState.queryRunning = false;
  if (previousQueryId) void backend.cancelQuery(previousQueryId).catch(() => undefined);

  databaseState.activeConnectionId = connectionId || null;
  clearSchema();
  databaseState.error = null;
  if (!connectionId) {
    databaseState.schemaLoading = false;
    return;
  }

  databaseState.schemaLoading = true;
  try {
    const schemas = await backend.listSchemas(connectionId);
    if (generation !== schemaGeneration || databaseState.activeConnectionId !== connectionId) return;
    databaseState.schemas = schemas ?? [];
    const preferred = databaseState.connections.find(({ id }) => id === connectionId)?.defaultSchema;
    const schema = databaseState.schemas.some(({ name }) => name === preferred)
      ? preferred!
      : databaseState.schemas[0]?.name ?? "";
    databaseState.activeSchema = schema || null;
    if (schema) {
      const tables = await backend.listTables(connectionId, schema);
      if (generation !== schemaGeneration || databaseState.activeConnectionId !== connectionId) return;
      databaseState.tables = tables ?? [];
    }
  } catch (error: unknown) {
    if (generation === schemaGeneration) databaseState.error = errorMessage(error);
  } finally {
    if (generation === schemaGeneration) databaseState.schemaLoading = false;
  }
}

export async function selectDatabaseSchema(schema: string): Promise<void> {
  const connectionId = databaseState.activeConnectionId;
  const generation = ++schemaGeneration;
  databaseState.activeSchema = schema || null;
  databaseState.tables = [];
  databaseState.selectedTable = null;
  databaseState.columns = [];
  databaseState.result = null;
  databaseState.error = null;
  if (!connectionId || !schema) return;

  databaseState.schemaLoading = true;
  try {
    const tables = await backend.listTables(connectionId, schema);
    if (
      generation !== schemaGeneration
      || databaseState.activeConnectionId !== connectionId
      || databaseState.activeSchema !== schema
    ) return;
    databaseState.tables = tables ?? [];
  } catch (error: unknown) {
    if (generation === schemaGeneration) databaseState.error = errorMessage(error);
  } finally {
    if (generation === schemaGeneration) databaseState.schemaLoading = false;
  }
}

export async function selectDatabaseTable(table: string): Promise<void> {
  const connectionId = databaseState.activeConnectionId;
  const schema = databaseState.activeSchema;
  const generation = ++schemaGeneration;
  databaseState.selectedTable = table || null;
  databaseState.columns = [];
  databaseState.error = null;
  if (!connectionId || !schema || !table) return;

  databaseState.schemaLoading = true;
  const provider = activeDatabaseConnection.value?.provider ?? "sqlite";
  const quote = provider === "mysql"
    ? (value: string) => `\`${value.replaceAll("`", "``")}\``
    : (value: string) => `"${value.replaceAll('"', '""')}"`;
  const tableReference = provider === "sqlite" ? quote(table) : `${quote(schema)}.${quote(table)}`;
  databaseState.sql = `SELECT * FROM ${tableReference}`;
  try {
    const columns = await backend.describeTable(connectionId, schema, table);
    if (
      generation !== schemaGeneration
      || databaseState.activeConnectionId !== connectionId
      || databaseState.activeSchema !== schema
      || databaseState.selectedTable !== table
    ) return;
    databaseState.columns = columns ?? [];
  } catch (error: unknown) {
    if (generation === schemaGeneration) databaseState.error = errorMessage(error);
  } finally {
    if (generation === schemaGeneration) databaseState.schemaLoading = false;
  }
}

export async function runDatabaseQuery(page = 0): Promise<DatabaseQueryResult | null> {
  const connectionId = databaseState.activeConnectionId;
  const sql = databaseState.sql.trim();
  if (!connectionId) {
    databaseState.error = "Select a database connection first";
    return null;
  }
  if (!sql) {
    databaseState.error = "SQL query is required";
    return null;
  }

  const generation = ++queryGeneration;
  const previousQueryId = databaseState.activeQueryId;
  if (previousQueryId) void backend.cancelQuery(previousQueryId).catch(() => undefined);
  const requestId = createID("query");
  databaseState.activeQueryId = requestId;
  databaseState.queryRunning = true;
  databaseState.error = null;
  try {
    const result = await backend.queryPage({
      requestId,
      connectionId,
      sql,
      parameters: [],
      page: Math.max(0, page),
      pageSize: databaseState.pageSize,
    });
    if (
      generation !== queryGeneration
      || databaseState.activeConnectionId !== connectionId
      || databaseState.activeQueryId !== requestId
    ) return result;
    databaseState.result = result;
    return result;
  } catch (error: unknown) {
    if (generation === queryGeneration) databaseState.error = errorMessage(error);
    return null;
  } finally {
    if (generation === queryGeneration && databaseState.activeQueryId === requestId) {
      databaseState.activeQueryId = null;
      databaseState.queryRunning = false;
    }
  }
}

export async function cancelDatabaseQuery(): Promise<boolean> {
  const requestId = databaseState.activeQueryId;
  if (!requestId) return false;
  queryGeneration += 1;
  databaseState.activeQueryId = null;
  databaseState.queryRunning = false;
  try {
    return await backend.cancelQuery(requestId);
  } catch (error: unknown) {
    databaseState.error = errorMessage(error);
    return false;
  }
}

export async function disconnectDatabase(connectionId: string): Promise<boolean> {
  if (!connectionId) return false;
  if (databaseState.activeConnectionId === connectionId) await cancelDatabaseQuery();
  databaseState.error = null;
  try {
    await backend.disconnect(connectionId);
    databaseState.connections = databaseState.connections.filter(({ id }) => id !== connectionId);
    if (databaseState.activeConnectionId === connectionId) {
      const next = databaseState.connections[0]?.id ?? "";
      await selectDatabaseConnection(next);
    }
    return true;
  } catch (error: unknown) {
    databaseState.error = errorMessage(error);
    return false;
  }
}

export function resetDatabaseStore(): void {
  schemaGeneration += 1;
  queryGeneration += 1;
  connectionSequence = 0;
  databaseState.connections = [];
  databaseState.activeConnectionId = null;
  clearSchema();
  databaseState.sql = "SELECT 1";
  databaseState.pageSize = 100;
  databaseState.connecting = false;
  databaseState.schemaLoading = false;
  databaseState.queryRunning = false;
  databaseState.activeQueryId = null;
  databaseState.error = null;
  backend = databaseService;
}
