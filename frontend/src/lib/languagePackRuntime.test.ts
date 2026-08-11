import { afterEach, describe, expect, it } from "vitest";
import {
  detectLanguage,
  replaceExternalLanguagePackContributions,
} from "./language";
import {
  createBuiltInLanguagePackRegistry,
  LanguagePackRegistry,
  validateLanguagePackManifest,
  validateLanguagePackRuntimeContribution,
} from "./languagePackRuntime";

const validManifest = {
  schemaVersion: "1.0",
  id: "org.example.test",
  version: "1.2.3",
  displayName: "Test",
  compatibility: {
    engineApi: "1.0",
    hostProtocol: "language.local.v1",
    platforms: [{ os: "windows", arch: "amd64" }],
  },
  languages: [{ id: "testlang", extensions: [".test"], filenames: [] }],
  rootMarkers: ["test.config"],
  servers: [],
  permissions: ["workspace.read"],
  configurationSchema: {},
  integrity: { manifestSha256: "a".repeat(64) },
};

describe("language pack runtime", () => {
  afterEach(() => replaceExternalLanguagePackContributions([]));

  it("registers built-in Go and TypeScript packs and detects their files", () => {
    const registry = createBuiltInLanguagePackRegistry();

    expect(registry.list()).toHaveLength(2);
    expect(registry.list()[0]?.compatibility).toMatchObject({
      engineApi: "1.0",
      hostProtocol: "language.local.v1",
    });
    expect(registry.list()[0]?.servers[0]).toMatchObject({
      id: "gopls",
      args: ["serve"],
      initializationProfile: "go",
    });
    expect(registry.list()[1]?.servers).toHaveLength(2);
    expect(registry.list()[0]?.toolchain?.commands).toEqual(
      expect.arrayContaining([
        expect.objectContaining({ id: "go-build", executable: "go" }),
      ]),
    );
    expect(registry.list()[1]?.toolchain?.commands).toEqual(
      expect.arrayContaining([
        expect.objectContaining({ id: "prettier-file", fileScoped: true }),
      ]),
    );
    expect(registry.list()[0]?.debuggers).toEqual([
      expect.objectContaining({
        id: "delve",
        protocol: "dap",
        executable: "dlv",
        args: ["dap", "--log=false"],
      }),
    ]);
    expect(registry.list()[1]?.debuggers).toEqual([
      expect.objectContaining({
        id: "node-inspector",
        protocol: "cdp",
        executable: "node",
        args: ["--inspect-brk"],
      }),
    ]);
    expect(registry.detect("cmd/main.go")).toBe("go");
    expect(registry.detect("src/app.tsx")).toBe("typescriptreact");
    expect(registry.detect("src/app.mjs")).toBe("javascript");
    expect(registry.detect("README.unknown")).toBeNull();
  });

  it("keeps executable declarations immutable and rejects path commands", () => {
    const manifest = createBuiltInLanguagePackRegistry().list()[0]!;
    expect(Object.isFrozen(manifest.servers)).toBe(true);
    expect(Object.isFrozen(manifest.servers[0]?.executables)).toBe(true);
    expect(Object.isFrozen(manifest.debuggers)).toBe(true);
    expect(Object.isFrozen(manifest.debuggers?.[0]?.args)).toBe(true);

    expect(() =>
      validateLanguagePackManifest({
        ...manifest,
        servers: [
          {
            ...manifest.servers[0],
            executables: [
              { commandName: "C:\\tools\\gopls.exe", kind: "gopls" },
            ],
          },
        ],
      }),
    ).toThrow("unsafe executable");
  });

  it("rejects server permission elevation and unknown language aliases", () => {
    const manifest = createBuiltInLanguagePackRegistry().list()[0]!;
    expect(() =>
      validateLanguagePackManifest({
        ...manifest,
        permissions: ["workspace.read", "process.launch", "workspace.write"],
      }),
    ).toThrow("not supported");
    expect(() =>
      validateLanguagePackManifest({
        ...manifest,
        servers: [{ ...manifest.servers[0], aliases: ["python"] }],
      }),
    ).toThrow("unknown languages");
    expect(() =>
      validateLanguagePackManifest({
        ...manifest,
        servers: [
          { ...manifest.servers[0], versionExecutable: "C:\\tools\\gopls.exe" },
        ],
      }),
    ).toThrow("unsafe version executable");
    expect(() =>
      validateLanguagePackManifest({
        ...manifest,
        servers: [{ ...manifest.servers[0], versionPin: "latest" }],
      }),
    ).toThrow("invalid version pin");
  });

  it("rejects unknown fields and unsupported executable servers", () => {
    expect(() =>
      validateLanguagePackManifest({ ...validManifest, typo: true }),
    ).toThrow("unsupported field");
    expect(() =>
      validateLanguagePackManifest({
        ...validManifest,
        servers: [{ id: "server" }],
      }),
    ).toThrow("not supported");
  });

  it("rejects unsafe toolchain declarations", () => {
    const manifest = createBuiltInLanguagePackRegistry().list()[0]!;
    const toolchain = manifest.toolchain!;
    expect(() =>
      validateLanguagePackManifest({
        ...manifest,
        toolchain: {
          ...toolchain,
          commands: toolchain.commands.map((command, index) =>
            index === 0 ? { ...command, language: "python" } : command,
          ),
        },
      }),
    ).toThrow("unknown language");
    expect(() =>
      validateLanguagePackManifest({
        ...manifest,
        toolchain: {
          ...toolchain,
          commands: toolchain.commands.map((command, index) =>
            index === 0 ? { ...command, executable: "sh" } : command,
          ),
        },
      }),
    ).toThrow("undeclared tool");
    expect(() =>
      validateLanguagePackManifest({
        ...manifest,
        toolchain: {
          ...toolchain,
          commands: toolchain.commands.map((command, index) =>
            index === 0 ? { ...command, args: ["\0"] } : command,
          ),
        },
      }),
    ).toThrow("NUL");
    expect(() =>
      validateLanguagePackManifest({
        ...manifest,
        toolchain: {
          ...toolchain,
          tools: toolchain.tools.map((tool, index) =>
            index === 0 ? { ...tool, installHint: "" } : tool,
          ),
        },
      }),
    ).toThrow("non-empty string");
  });

  it("rejects unsafe debugger declarations", () => {
    const manifest = createBuiltInLanguagePackRegistry().list()[0]!;
    expect(() =>
      validateLanguagePackManifest({
        ...manifest,
        debuggers: manifest.debuggers!.map((debuggerSpec) => ({
          ...debuggerSpec,
          executable: "C:\\tools\\dlv.exe",
        })),
      }),
    ).toThrow("invalid or duplicated");
    expect(() =>
      validateLanguagePackManifest({
        ...manifest,
        debuggers: manifest.debuggers!.map((debuggerSpec) => ({
          ...debuggerSpec,
          protocol: "stdio",
        })),
      }),
    ).toThrow("invalid or duplicated");
    expect(() =>
      validateLanguagePackManifest({
        ...manifest,
        debuggers: manifest.debuggers!.map((debuggerSpec) => ({
          ...debuggerSpec,
          languages: ["python"],
        })),
      }),
    ).toThrow("unknown or duplicate language");
    expect(() =>
      validateLanguagePackManifest({
        ...manifest,
        debuggers: manifest.debuggers!.map((debuggerSpec) => ({
          ...debuggerSpec,
          args: ["\0"],
        })),
      }),
    ).toThrow("NUL");
  });

  it("rejects incompatible versions, duplicate languages, and invalid integrity", () => {
    expect(() =>
      validateLanguagePackManifest({ ...validManifest, schemaVersion: "2.0" }),
    ).toThrow("Unsupported language pack schema version");
    for (const version of ["01.0.0", "1.0.0-01", "1.0.0-alpha..1", "1.0.0-"]) {
      expect(() =>
        validateLanguagePackManifest({ ...validManifest, version }),
      ).toThrow("version is invalid");
    }
    expect(
      validateLanguagePackManifest({
        ...validManifest,
        version: "1.0.0-rc.1+windows.amd64",
      }).version,
    ).toBe("1.0.0-rc.1+windows.amd64");
    expect(() =>
      validateLanguagePackManifest({
        ...validManifest,
        compatibility: { ...validManifest.compatibility, engineApi: "2.0" },
      }),
    ).toThrow("Unsupported language pack engine API");
    expect(() =>
      validateLanguagePackManifest({
        ...validManifest,
        compatibility: {
          ...validManifest.compatibility,
          hostProtocol: "language.remote.v1",
        },
      }),
    ).toThrow("Unsupported language pack host protocol");
    expect(() =>
      validateLanguagePackManifest({
        ...validManifest,
        compatibility: {
          ...validManifest.compatibility,
          platforms: [
            { os: "windows", arch: "amd64" },
            { os: "windows", arch: "amd64" },
          ],
        },
      }),
    ).toThrow("Duplicate language pack platform");
    expect(() =>
      validateLanguagePackManifest({
        ...validManifest,
        languages: [
          { id: "same", extensions: [".a"], filenames: [] },
          { id: "same", extensions: [".b"], filenames: [] },
        ],
      }),
    ).toThrow("Duplicate language id");
    expect(() =>
      validateLanguagePackManifest({
        ...validManifest,
        languages: [
          { id: "one", extensions: [".same"], filenames: [] },
          { id: "two", extensions: [".same"], filenames: [] },
        ],
      }),
    ).toThrow("Ambiguous language extension");
    expect(() =>
      validateLanguagePackManifest({
        ...validManifest,
        integrity: { manifestSha256: "not-a-hash" },
      }),
    ).toThrow("SHA-256");
  });

  it("rejects duplicate pack IDs and cross-pack language collisions", () => {
    const registry = new LanguagePackRegistry();
    registry.register(validManifest);
    expect(() => registry.register(validManifest)).toThrow(
      "already registered",
    );
    expect(() =>
      registry.register({
        ...validManifest,
        id: "org.example.other",
        languages: [{ id: "testlang", extensions: [".other"], filenames: [] }],
      }),
    ).toThrow("already provided");
    expect(() =>
      registry.register({
        ...validManifest,
        id: "org.example.selector",
        languages: [{ id: "otherlang", extensions: [".test"], filenames: [] }],
      }),
    ).toThrow("already provided");
  });

  it("keeps legacy language detection for packs not yet migrated", () => {
    expect(detectLanguage("styles.css")).toBe("css");
    expect(detectLanguage("App.vue")).toBe("html");
    expect(detectLanguage("README.md")).toBe("markdown");
  });

  it("atomically activates and revokes external renderer selectors", () => {
    const contribution = {
      id: "org.example.kpython",
      version: "1.0.0",
      manifestSha256: "b".repeat(64),
      languages: [
        { id: "kpython", extensions: [".kpy"], filenames: ["Kpythonfile"] },
      ],
    };
    replaceExternalLanguagePackContributions([contribution]);
    expect(detectLanguage("src/main.kpy")).toBe("kpython");
    expect(detectLanguage("Kpythonfile")).toBe("kpython");

    expect(() =>
      replaceExternalLanguagePackContributions([
        contribution,
        {
          id: "org.example.hostile",
          version: "1.0.0",
          manifestSha256: "c".repeat(64),
          languages: [{ id: "hostile", extensions: [".go"], filenames: [] }],
        },
      ]),
    ).toThrow("already provided");
    expect(detectLanguage("src/main.kpy")).toBe("kpython");

    replaceExternalLanguagePackContributions([]);
    expect(detectLanguage("src/main.kpy")).toBe("plaintext");
  });

  it("rejects process fields and malformed selectors in renderer snapshots", () => {
    const contribution = {
      id: "org.example.hostile",
      version: "1.0.0",
      manifestSha256: "d".repeat(64),
      languages: [{ id: "hostile", extensions: [".hostile"], filenames: [] }],
    };
    expect(() =>
      validateLanguagePackRuntimeContribution({
        ...contribution,
        executable: "cmd.exe",
      }),
    ).toThrow("unsupported field");
    expect(() =>
      validateLanguagePackRuntimeContribution({
        ...contribution,
        languages: [
          { id: "hostile", extensions: ["../hostile"], filenames: [] },
        ],
      }),
    ).toThrow("extension is invalid");
  });
});
