import { afterEach, describe, expect, it } from "vitest";
import {
  executeCommand,
  remoteState,
  resetRemoteStore,
  setRemoteBackend,
  type RemoteBackend,
} from "./remote";

function makeBackend(
  run: (name: string, argv: string[], approvalToken: string) => Promise<string>,
): RemoteBackend {
  return {
    connect: async () => undefined,
    disconnect: async () => undefined,
    isConnected: async () => false,
    requestCommandApproval: async () => "backend-approval-token",
    executeCommand: run,
    listConnections: async () => [],
  };
}

afterEach(() => {
  resetRemoteStore();
});

describe("remote executeCommand", () => {
  it("passes the argv array to the backend unchanged", async () => {
    const argv = ["ls", "-1", "--", "/srv/project with spaces"];
    let receivedName: string | undefined;
    let receivedArgv: string[] | undefined;
    let receivedToken: string | undefined;
    setRemoteBackend(
      makeBackend(async (name, commandArgv, approvalToken) => {
        receivedName = name;
        receivedArgv = commandArgv;
        receivedToken = approvalToken;
        return "src\n";
      }),
    );

    await expect(executeCommand("project", argv)).resolves.toBe("src\n");
    expect(receivedName).toBe("project");
    expect(receivedArgv).toBe(argv);
    expect(receivedToken).toBe("backend-approval-token");
  });

  it("records backend errors and returns null", async () => {
    setRemoteBackend(
      makeBackend(async () => {
        throw new Error("remote command blocked");
      }),
    );

    await expect(executeCommand("project", ["ls"])).resolves.toBeNull();
    expect(remoteState.error).toContain("remote command blocked");
  });
});
