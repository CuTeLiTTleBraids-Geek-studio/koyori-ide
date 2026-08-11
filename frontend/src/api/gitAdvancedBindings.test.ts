import { beforeEach, describe, expect, it, vi } from "vitest";

const runtime = vi.hoisted(() => ({
  byID: vi.fn(),
}));

vi.mock("@wailsio/runtime", () => ({
  Call: { ByID: runtime.byID },
}));

import * as GitRebaseService from "../../bindings/github.com/CuTeLiTTleBraids-Geek-studio/koyori-ide/services/gitrebaseservice.js";
import * as GitWorktreeService from "../../bindings/github.com/CuTeLiTTleBraids-Geek-studio/koyori-ide/services/gitworktreeservice.js";

describe("generated advanced Git bindings", () => {
  beforeEach(() => {
    runtime.byID.mockReset().mockResolvedValue(undefined);
  });

  it("exports every worktree operation through ByID", async () => {
    await GitWorktreeService.ListWorktrees("C:/repo");
    await GitWorktreeService.AddWorktree("C:/repo", "C:/linked", "main", {
      newBranch: "",
      detach: false,
      force: false,
      noCheckout: false,
    });
    await GitWorktreeService.RemoveWorktree("C:/repo", "C:/linked", false);
    await GitWorktreeService.PruneWorktrees("C:/repo", true);
    await GitWorktreeService.LockWorktree("C:/repo", "C:/linked", "maintenance");
    await GitWorktreeService.UnlockWorktree("C:/repo", "C:/linked");
    await GitWorktreeService.MoveWorktree("C:/repo", "C:/linked", "C:/moved", false);

    expect(runtime.byID).toHaveBeenCalledTimes(7);
  });

  it("exports the complete interactive rebase workflow through ByID", async () => {
    await GitRebaseService.GetRebaseTodoList("C:/repo", "main");
    await GitRebaseService.GetRebaseStatus("C:/repo");
    await GitRebaseService.StartInteractiveRebase("C:/repo", "main");
    await GitRebaseService.ApplyRebaseActions("C:/repo", []);
    await GitRebaseService.ContinueRebase("C:/repo");
    await GitRebaseService.AbortRebase("C:/repo");
    await GitRebaseService.SkipCommit("C:/repo");
    await GitRebaseService.IsRebaseInProgress("C:/repo");

    expect(runtime.byID).toHaveBeenCalledTimes(8);
  });
});
