import { beforeEach, describe, expect, it, vi } from "vitest";
import { flushPromises, mount } from "@vue/test-utils";

const { getCommitGraph } = vi.hoisted(() => ({
  getCommitGraph: vi.fn(),
}));

vi.mock("@/api/services", () => ({
  gitService: { getCommitGraph },
}));

import CommitGraph from "./CommitGraph.vue";

const commits = [
  {
    hash: "3333333333333333333333333333333333333333",
    parents: [
      "2222222222222222222222222222222222222222",
      "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
    ],
    author: "Mira Chen",
    email: "mira@example.com",
    time: "2026-07-16T01:30:00Z",
    refs: ["HEAD -> main", "tag: v1.2.0"],
    subject: "Merge release branch",
  },
  {
    hash: "2222222222222222222222222222222222222222",
    parents: ["1111111111111111111111111111111111111111"],
    author: "Mira Chen",
    email: "mira@example.com",
    time: "2026-07-16T01:00:00Z",
    refs: [],
    subject: "Tighten validation",
  },
  {
    hash: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
    parents: ["1111111111111111111111111111111111111111"],
    author: "Kai Wu",
    email: "kai@example.com",
    time: "2026-07-15T23:00:00Z",
    refs: ["release"],
    subject: "Prepare release",
  },
  {
    hash: "1111111111111111111111111111111111111111",
    parents: [],
    author: "Mira Chen",
    email: "mira@example.com",
    time: "2026-07-15T20:00:00Z",
    refs: [],
    subject: "Initial commit",
  },
];

function deferred<T>() {
  let resolve!: (value: T | PromiseLike<T>) => void;
  const promise = new Promise<T>((resolvePromise) => {
    resolve = resolvePromise;
  });
  return { promise, resolve };
}

describe("8B: CommitGraph", () => {
  beforeEach(() => {
    getCommitGraph.mockReset();
    getCommitGraph.mockResolvedValue(commits);
  });

  it("loads the current branch with the default limit and derives lanes from parents", async () => {
    const wrapper = mount(CommitGraph, {
      props: { repoPath: "/repo", branch: "main" },
    });
    await flushPromises();

    expect(getCommitGraph).toHaveBeenCalledWith("/repo", 50, "main", false);
    const rows = wrapper.findAll(".commit-graph__row");
    expect(rows).toHaveLength(4);
    expect(rows.map((row) => row.attributes("data-lane"))).toEqual(["0", "0", "1", "0"]);
    expect(wrapper.text()).toContain("Merge release branch");
    expect(wrapper.text()).toContain("v1.2.0");
  });

  it("shows the selected commit details inline", async () => {
    const wrapper = mount(CommitGraph, {
      props: { repoPath: "/repo", branch: "main" },
    });
    await flushPromises();

    await wrapper.find('[data-hash="3333333333333333333333333333333333333333"]').trigger("click");

    const details = wrapper.find(".commit-graph__details");
    expect(details.text()).toContain("3333333333333333333333333333333333333333");
    expect(details.text()).toContain("2222222222222222222222222222222222222222");
    expect(details.text()).toContain("Mira Chen");
    expect(details.text()).toContain("mira@example.com");
    expect(details.text()).toContain("HEAD -> main");
    expect(details.text()).toContain("Merge release branch");
    expect(details.find("time").attributes("datetime")).toBe("2026-07-16T01:30:00Z");
  });

  it("reloads for all branches and clamps the selectable limit to 200", async () => {
    const wrapper = mount(CommitGraph, {
      props: { repoPath: "/repo", branch: "main" },
    });
    await flushPromises();
    getCommitGraph.mockClear();

    await wrapper.find('[data-scope="all"]').trigger("click");
    await flushPromises();
    expect(getCommitGraph).toHaveBeenLastCalledWith("/repo", 50, "", true);

    await wrapper.find(".commit-graph__limit").setValue("200");
    await flushPromises();
    expect(getCommitGraph).toHaveBeenLastCalledWith("/repo", 200, "", true);
    expect(wrapper.findAll(".commit-graph__limit option").map((option) => option.attributes("value")))
      .toEqual(["25", "50", "100", "200"]);
  });

  it("ignores an older load that resolves after the repository changes", async () => {
    const oldLoad = deferred<typeof commits>();
    const newLoad = deferred<typeof commits>();
    getCommitGraph
      .mockImplementationOnce(() => oldLoad.promise)
      .mockImplementationOnce(() => newLoad.promise);
    const wrapper = mount(CommitGraph, {
      props: { repoPath: "/old", branch: "main" },
    });

    await wrapper.setProps({ repoPath: "/new" });
    newLoad.resolve([{ ...commits[0], subject: "Newest repository" }]);
    await flushPromises();
    expect(wrapper.text()).toContain("Newest repository");

    oldLoad.resolve([{ ...commits[0], subject: "Stale repository" }]);
    await flushPromises();
    expect(wrapper.text()).not.toContain("Stale repository");
    expect(wrapper.text()).toContain("Newest repository");
  });

  it("shows an empty state for a repository without commits", async () => {
    getCommitGraph.mockResolvedValue([]);
    const wrapper = mount(CommitGraph, {
      props: { repoPath: "/repo", branch: "main" },
    });
    await flushPromises();

    expect(wrapper.find(".commit-graph__empty").text()).toContain("No commits yet");
  });
});
