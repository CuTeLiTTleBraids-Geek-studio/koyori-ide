import { describe, it, expect, beforeEach, vi } from "vitest";
import { mount } from "@vue/test-utils";
import type { App } from "vue";
import ElementPlus from "element-plus";

// Use vi.hoisted so the mock factories can reference these variables —
// vi.mock calls are hoisted to the top of the file, before any const
// declarations, so plain top-level consts would be in the temporal dead zone.
const {
  mockTemplates,
  listProjectTemplatesMock,
  createProjectMock,
  pickDirectoryMock,
  notifyErrorMock,
} = vi.hoisted(() => ({
  mockTemplates: [
    { id: "go", name: "Go Service", description: "HTTP server", language: "Go" },
    { id: "typescript", name: "TypeScript Project", description: "TS app", language: "TypeScript" },
    { id: "javascript", name: "JavaScript Project", description: "JS app", language: "JavaScript" },
    { id: "monorepo", name: "Monorepo", description: "pnpm workspace", language: "TypeScript" },
    { id: "fullstack", name: "Fullstack", description: "Go + Vue", language: "Go + TypeScript" },
  ],
  listProjectTemplatesMock: vi.fn().mockResolvedValue([]),
  createProjectMock: vi.fn().mockResolvedValue("/tmp/demo"),
  pickDirectoryMock: vi.fn().mockResolvedValue(""),
  notifyErrorMock: vi.fn(),
}));

vi.mock("@/api/services", () => ({
  projectService: {
    listProjectTemplates: listProjectTemplatesMock,
    createProject: createProjectMock,
  },
  fileService: {
    pickDirectory: pickDirectoryMock,
  },
}));

vi.mock("@/lib/errors", () => ({
  errorMessage: (e: unknown) => (e instanceof Error ? e.message : String(e)),
}));

vi.mock("@/lib/notifications", () => ({
  notifyError: notifyErrorMock,
}));

vi.mock("@/lib/i18n", () => ({
  useI18n: () => ({
    t: (key: string) => {
      const map: Record<string, string> = {
        "newProject.title": "New Project",
        "newProject.stepSelect": "Select Template",
        "newProject.stepDetails": "Project Details",
        "newProject.stepConfirm": "Confirm",
        "newProject.selectTemplate": "Choose a template",
        "newProject.projectName": "Project Name",
        "newProject.projectNamePlaceholder": "my-project",
        "newProject.targetDir": "Target Directory",
        "newProject.targetDirPlaceholder": "Parent dir",
        "newProject.moduleName": "Module Name",
        "newProject.moduleNamePlaceholder": "github.com/user/repo",
        "newProject.moduleNameHint": "Go module path",
        "newProject.confirm": "Review and create.",
        "newProject.creating": "Creating...",
        "newProject.createFailed": "Failed: {error}",
        "newProject.created": "Project created",
        "newProject.openProject": "Open Project",
        "common.cancel": "Cancel",
        "common.back": "Back",
        "common.next": "Next",
        "common.create": "Create",
        "common.browse": "Browse",
        "common.loading": "Loading...",
      };
      return map[key] ?? key;
    },
    locale: { value: "en" },
  }),
}));

const iconPlugin = {
  install(_app: App) {
    // ElementPlus icons not needed for this test; no-op.
  },
};

// Import the component AFTER the mocks are set up.
const WizardModule = await import("./NewProjectWizard.vue");
const NewProjectWizard = WizardModule.default;

function mountWizard(visible = true) {
  return mount(NewProjectWizard, {
    props: { visible },
    global: {
      plugins: [ElementPlus, iconPlugin],
    },
  });
}

async function flush() {
  // Flush pending microtasks (async setup / watch callbacks).
  await new Promise((r) => setTimeout(r, 10));
}

describe("NewProjectWizard (G-FEAT-01)", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    listProjectTemplatesMock.mockResolvedValue([...mockTemplates]);
    createProjectMock.mockResolvedValue("/tmp/demo");
    pickDirectoryMock.mockResolvedValue("");
  });

  it("does not render when visible is false", () => {
    const wrapper = mountWizard(false);
    expect(wrapper.find(".npw").exists()).toBe(false);
  });

  it("loads templates and renders the selection cards on step 1", async () => {
    const wrapper = mountWizard(true);
    await flush();
    expect(listProjectTemplatesMock).toHaveBeenCalled();
    const cards = wrapper.findAll(".npw__card");
    expect(cards).toHaveLength(5);
    expect(cards[0].text()).toContain("Go Service");
  });

  it("Next button is disabled until a template is selected", async () => {
    const wrapper = mountWizard(true);
    await flush();
    const nextBtn = wrapper.find(".npw__btn--primary");
    expect(nextBtn.attributes("disabled")).toBeDefined();
  });

  it("selecting a template enables Next and advances to step 2", async () => {
    const wrapper = mountWizard(true);
    await flush();
    const cards = wrapper.findAll(".npw__card");
    await cards[0].trigger("click");
    // The Go card should now be marked selected.
    expect(cards[0].classes()).toContain("npw__card--selected");
    // Click Next.
    const nextBtn = wrapper.find(".npw__btn--primary");
    expect(nextBtn.attributes("disabled")).toBeUndefined();
    await nextBtn.trigger("click");
    // Step 2 should show the project name input.
    expect(wrapper.find('input[aria-label="Project Name"]').exists()).toBe(true);
  });

  it("step 2 shows module name field only for Go/Fullstack templates", async () => {
    const wrapper = mountWizard(true);
    await flush();
    // Select TypeScript (no module name needed).
    const tsCard = wrapper.findAll(".npw__card")[1];
    await tsCard.trigger("click");
    await wrapper.find(".npw__btn--primary").trigger("click");
    expect(wrapper.find('input[aria-label="Module Name"]').exists()).toBe(false);

    // Go back and select Go. The Back button is the second .npw__btn
    // (Cancel is the first, with the --ghost modifier).
    const backBtn = wrapper.findAll(".npw__btn").find((b) => b.text() === "Back");
    expect(backBtn).toBeTruthy();
    await backBtn!.trigger("click");
    const goCard = wrapper.findAll(".npw__card")[0];
    await goCard.trigger("click");
    await wrapper.find(".npw__btn--primary").trigger("click");
    expect(wrapper.find('input[aria-label="Module Name"]').exists()).toBe(true);
  });

  it("Next on step 2 is disabled until project name (and module name for Go) are filled", async () => {
    const wrapper = mountWizard(true);
    await flush();
    // Select Go.
    await wrapper.findAll(".npw__card")[0].trigger("click");
    await wrapper.find(".npw__btn--primary").trigger("click");
    // Both empty → disabled.
    let nextBtn = wrapper.find(".npw__btn--primary");
    expect(nextBtn.attributes("disabled")).toBeDefined();

    // Fill project name only → still disabled (module name required for Go).
    await wrapper.find('input[aria-label="Project Name"]').setValue("demo");
    nextBtn = wrapper.find(".npw__btn--primary");
    expect(nextBtn.attributes("disabled")).toBeDefined();

    // Fill module name → enabled.
    await wrapper.find('input[aria-label="Module Name"]').setValue("github.com/x/demo");
    nextBtn = wrapper.find(".npw__btn--primary");
    expect(nextBtn.attributes("disabled")).toBeUndefined();
  });

  it("step 3 shows a summary and the Create button", async () => {
    const wrapper = mountWizard(true);
    await flush();
    // Go template.
    await wrapper.findAll(".npw__card")[0].trigger("click");
    await wrapper.find(".npw__btn--primary").trigger("click");
    await wrapper.find('input[aria-label="Project Name"]').setValue("demo");
    await wrapper.find('input[aria-label="Module Name"]').setValue("github.com/x/demo");
    await wrapper.find(".npw__btn--primary").trigger("click");
    // Step 3: summary rows.
    const rows = wrapper.findAll(".npw__summary-row");
    expect(rows.length).toBeGreaterThanOrEqual(3);
    expect(wrapper.text()).toContain("demo");
    expect(wrapper.text()).toContain("github.com/x/demo");
    // Create button present.
    const createBtn = wrapper.find(".npw__btn--primary");
    expect(createBtn.text()).toBe("Create");
  });

  it("clicking Create calls createProject and shows the success state", async () => {
    createProjectMock.mockResolvedValue("/tmp/demo");
    const wrapper = mountWizard(true);
    await flush();
    // Go template → step 2 → step 3.
    await wrapper.findAll(".npw__card")[0].trigger("click");
    await wrapper.find(".npw__btn--primary").trigger("click");
    await wrapper.find('input[aria-label="Project Name"]').setValue("demo");
    await wrapper.find('input[aria-label="Module Name"]').setValue("github.com/x/demo");
    await wrapper.find(".npw__btn--primary").trigger("click");
    // Click Create.
    await wrapper.find(".npw__btn--primary").trigger("click");
    await flush();
    expect(createProjectMock).toHaveBeenCalledWith({
      templateId: "go",
      projectName: "demo",
      targetDir: "",
      moduleName: "github.com/x/demo",
    });
    // Success state should be visible.
    expect(wrapper.find(".npw__success").exists()).toBe(true);
    expect(wrapper.text()).toContain("/tmp/demo");
  });

  it("Create failure shows an error message and does not advance", async () => {
    createProjectMock.mockRejectedValue(new Error("boom"));
    const wrapper = mountWizard(true);
    await flush();
    await wrapper.findAll(".npw__card")[0].trigger("click");
    await wrapper.find(".npw__btn--primary").trigger("click");
    await wrapper.find('input[aria-label="Project Name"]').setValue("demo");
    await wrapper.find('input[aria-label="Module Name"]').setValue("github.com/x/demo");
    await wrapper.find(".npw__btn--primary").trigger("click");
    await wrapper.find(".npw__btn--primary").trigger("click");
    await flush();
    expect(wrapper.find(".npw__error").exists()).toBe(true);
    expect(wrapper.text()).toContain("boom");
    expect(notifyErrorMock).toHaveBeenCalled();
    // Should still be on step 3 without success state.
    expect(wrapper.find(".npw__success").exists()).toBe(false);
  });

  it("emits created with the path when Open Project is clicked", async () => {
    createProjectMock.mockResolvedValue("/tmp/demo");
    const wrapper = mountWizard(true);
    await flush();
    await wrapper.findAll(".npw__card")[0].trigger("click");
    await wrapper.find(".npw__btn--primary").trigger("click");
    await wrapper.find('input[aria-label="Project Name"]').setValue("demo");
    await wrapper.find('input[aria-label="Module Name"]').setValue("github.com/x/demo");
    await wrapper.find(".npw__btn--primary").trigger("click");
    await wrapper.find(".npw__btn--primary").trigger("click"); // Create
    await flush();
    // Now the Open Project button should be visible.
    const openBtn = wrapper.find(".npw__btn--primary");
    expect(openBtn.text()).toBe("Open Project");
    await openBtn.trigger("click");
    const createdEvents = wrapper.emitted("created");
    expect(createdEvents).toBeTruthy();
    expect(createdEvents![0]).toEqual(["/tmp/demo"]);
    // Should also emit close.
    expect(wrapper.emitted("close")).toBeTruthy();
  });

  it("Escape key emits close", async () => {
    const wrapper = mountWizard(true);
    await flush();
    await wrapper.find(".npw").trigger("keydown", { key: "Escape" });
    expect(wrapper.emitted("close")).toBeTruthy();
  });

  it("Cancel button emits close", async () => {
    const wrapper = mountWizard(true);
    await flush();
    // The ghost (cancel) button is the first button in the footer.
    const cancelBtn = wrapper.find(".npw__btn--ghost");
    await cancelBtn.trigger("click");
    expect(wrapper.emitted("close")).toBeTruthy();
  });
});
