<script setup lang="ts">
// Koyori IDE 组件 · New Project Wizard；交互服务：文件系统（FileService）、项目（ProjectService）。
// 喵，这是 New Project Wizard，负责 Koyori IDE 的界面呈现喵~
// G-FEAT-01: New Project scaffolding wizard.
//
// A 3-step modal wizard:
//   1. Select a template (Go / TypeScript / JavaScript / Monorepo / Fullstack)
//   2. Enter project name, target directory, and module name (Go only)
//   3. Confirm and create
//
// On success it emits "created" with the generated project path so the parent
// can open the new project workspace. Templates and generation happen on the
// Go backend (services/project_service.go) via embedded template files.

import { ref, computed, watch } from "vue";
import { projectService, fileService } from "@/api/services";
import { errorMessage } from "@/lib/errors";
import { notifyError } from "@/lib/notifications";
import { useI18n } from "@/lib/i18n";
import type { ProjectTemplate } from "@/types";
import FocusTrapDialog from "@/components/common/FocusTrapDialog.vue";

const props = defineProps<{
  visible: boolean;
}>();

const emit = defineEmits<{
  (e: "close"): void;
  (e: "created", path: string): void;
}>();

const { t } = useI18n();

type Step = 1 | 2 | 3;
const step = ref<Step>(1);
const templates = ref<ProjectTemplate[]>([]);
const loadingTemplates = ref(false);
const selectedTemplateId = ref<string>("");
const projectName = ref("");
const targetDir = ref("");
const moduleName = ref("");
const creating = ref(false);
const createdPath = ref<string | null>(null);
const errorMsg = ref<string | null>(null);

const selectedTemplate = computed(
  () => templates.value.find((tp) => tp.id === selectedTemplateId.value) ?? null,
);

const needsModuleName = computed(
  () => selectedTemplateId.value === "go" || selectedTemplateId.value === "fullstack",
);

const canProceedFromStep1 = computed(() => selectedTemplateId.value !== "");
const canProceedFromStep2 = computed(() => {
  if (projectName.value.trim() === "") return false;
  if (needsModuleName.value && moduleName.value.trim() === "") return false;
  return true;
});

async function loadTemplates() {
  loadingTemplates.value = true;
  try {
    templates.value = await projectService.listProjectTemplates();
  } catch (e) {
    notifyError(t("newProject.createFailed", { error: errorMessage(e) }));
    templates.value = [];
  } finally {
    loadingTemplates.value = false;
  }
}

function resetWizard() {
  step.value = 1;
  selectedTemplateId.value = "";
  projectName.value = "";
  targetDir.value = "";
  moduleName.value = "";
  creating.value = false;
  createdPath.value = null;
  errorMsg.value = null;
}

async function browseDirectory() {
  try {
    const dir = await fileService.pickDirectory();
    if (dir) targetDir.value = dir;
  } catch {
    // User cancelled —ignore.
  }
}

function goNext() {
  if (step.value === 1 && canProceedFromStep1.value) {
    step.value = 2;
  } else if (step.value === 2 && canProceedFromStep2.value) {
    step.value = 3;
  }
}

function goBack() {
  if (step.value > 1) step.value = (step.value - 1) as Step;
}

async function handleCreate() {
  creating.value = true;
  errorMsg.value = null;
  try {
    const path = await projectService.createProject({
      templateId: selectedTemplateId.value,
      projectName: projectName.value.trim(),
      targetDir: targetDir.value.trim(),
      moduleName: moduleName.value.trim(),
    });
    createdPath.value = path;
  } catch (e) {
    errorMsg.value = errorMessage(e);
    notifyError(t("newProject.createFailed", { error: errorMsg.value }));
  } finally {
    creating.value = false;
  }
}

function handleOpenCreated() {
  if (createdPath.value) {
    emit("created", createdPath.value);
  }
  emit("close");
}

watch(
  () => props.visible,
  (v) => {
    if (v) {
      resetWizard();
      void loadTemplates();
    }
  },
  { immediate: true },
);
</script>

<template>
  <transition name="npw-fade">
    <div
      v-if="visible"
      class="npw-overlay"
    >
      <button
        type="button"
        class="dialog-backdrop-button"
        tabindex="-1"
        :aria-label="t('a11y.closeDialog')"
        @click="emit('close')"
      />
      <FocusTrapDialog
        class="npw"
        :aria-label="t('newProject.title')"
        @close="emit('close')"
      >
        <header class="npw__header">
          <h2 class="npw__title">{{ t("newProject.title") }}</h2>
          <div class="npw__steps">
            <span
              v-for="s in [1, 2, 3]"
              :key="s"
              class="npw__step"
              :class="{
                'npw__step--active': step === s,
                'npw__step--done': step > s,
              }"
            >
              {{ s }}
            </span>
          </div>
        </header>

        <!-- Step 1: Select template -->
        <section v-if="step === 1" class="npw__body">
          <p class="npw__hint">{{ t("newProject.selectTemplate") }}</p>
          <div v-if="loadingTemplates" class="npw__loading">{{ t("common.loading") }}</div>
          <div v-else class="npw__cards">
            <button
              v-for="tp in templates"
              :key="tp.id"
              type="button"
              class="npw__card"
              :class="{ 'npw__card--selected': selectedTemplateId === tp.id }"
              :aria-pressed="selectedTemplateId === tp.id"
              @click="selectedTemplateId = tp.id"
            >
              <span class="npw__card-lang">{{ tp.language }}</span>
              <span class="npw__card-name">{{ tp.name }}</span>
              <span class="npw__card-desc">{{ tp.description }}</span>
            </button>
          </div>
        </section>

        <!-- Step 2: Project details -->
        <section v-else-if="step === 2" class="npw__body">
          <label class="npw__field">
            <span class="npw__label">{{ t("newProject.projectName") }}</span>
            <input
              v-model="projectName"
              class="npw__input"
              :placeholder="t('newProject.projectNamePlaceholder')"
              :aria-label="t('newProject.projectName')"
            />
          </label>
          <label class="npw__field">
            <span class="npw__label">{{ t("newProject.targetDir") }}</span>
            <div class="npw__dir-row">
              <input
                v-model="targetDir"
                class="npw__input"
                :placeholder="t('newProject.targetDirPlaceholder')"
                :aria-label="t('newProject.targetDir')"
              />
              <button type="button" class="npw__browse" @click="browseDirectory">
                {{ t("common.browse") }}
              </button>
            </div>
          </label>
          <label v-if="needsModuleName" class="npw__field">
            <span class="npw__label">{{ t("newProject.moduleName") }}</span>
            <input
              v-model="moduleName"
              class="npw__input"
              :placeholder="t('newProject.moduleNamePlaceholder')"
              :aria-label="t('newProject.moduleName')"
            />
            <span class="npw__hint npw__hint--small">{{ t("newProject.moduleNameHint") }}</span>
          </label>
        </section>

        <!-- Step 3: Confirm -->
        <section v-else class="npw__body">
          <p v-if="!createdPath" class="npw__hint">{{ t("newProject.confirm") }}</p>
          <dl class="npw__summary">
            <div class="npw__summary-row">
              <dt>{{ t("newProject.stepSelect") }}</dt>
              <dd>{{ selectedTemplate?.name ?? selectedTemplateId }}</dd>
            </div>
            <div class="npw__summary-row">
              <dt>{{ t("newProject.projectName") }}</dt>
              <dd>{{ projectName }}</dd>
            </div>
            <div class="npw__summary-row">
              <dt>{{ t("newProject.targetDir") }}</dt>
              <dd>{{ targetDir || "(temp)" }}</dd>
            </div>
            <div v-if="needsModuleName" class="npw__summary-row">
              <dt>{{ t("newProject.moduleName") }}</dt>
              <dd>{{ moduleName }}</dd>
            </div>
          </dl>
          <div v-if="createdPath" class="npw__success">
            <p>{{ t("newProject.created") }}</p>
            <code class="npw__path">{{ createdPath }}</code>
          </div>
          <div v-if="errorMsg" class="npw__error">{{ errorMsg }}</div>
        </section>

        <footer class="npw__footer">
          <button type="button" class="npw__btn npw__btn--ghost" @click="emit('close')">
            {{ t("common.cancel") }}
          </button>
          <template v-if="step > 1 && !createdPath">
            <button type="button" class="npw__btn" @click="goBack">
              {{ t("common.back") }}
            </button>
          </template>
          <template v-if="step < 3 && !createdPath">
            <button
              type="button"
              class="npw__btn npw__btn--primary"
              :disabled="(step === 1 && !canProceedFromStep1) || (step === 2 && !canProceedFromStep2)"
              @click="goNext"
            >
              {{ t("common.next") }}
            </button>
          </template>
          <template v-if="step === 3 && !createdPath">
            <button
              type="button"
              class="npw__btn npw__btn--primary"
              :disabled="creating"
              @click="handleCreate"
            >
              {{ creating ? t("newProject.creating") : t("common.create") }}
            </button>
          </template>
          <template v-if="createdPath">
            <button
              type="button"
              class="npw__btn npw__btn--primary"
              @click="handleOpenCreated"
            >
              {{ t("newProject.openProject") }}
            </button>
          </template>
        </footer>
      </FocusTrapDialog>
    </div>
  </transition>
</template>

<style scoped>
.npw-overlay {
  position: fixed;
  top: 0;
  left: 0;
  right: 0;
  bottom: 0;
  background-color: rgba(0, 0, 0, 0.4);
  z-index: 1000;
  display: flex;
  justify-content: center;
  align-items: center;
  padding: 24px;
}

.dialog-backdrop-button {
  position: absolute;
  inset: 0;
  width: 100%;
  height: 100%;
  margin: 0;
  padding: 0;
  border: 0;
  background: transparent;
  cursor: default;
}

.npw {
  position: relative;
  z-index: 1;
  width: 600px;
  max-width: 92vw;
  max-height: 85vh;
  display: flex;
  flex-direction: column;
  background-color: var(--color-bg-surface);
  border: 1px solid var(--color-border-default);
  border-radius: var(--radius-md);
  overflow: hidden;
  box-shadow: 0 8px 32px rgba(0, 0, 0, 0.3);
  outline: none;
}

.npw__header {
  display: flex;
  align-items: center;
  justify-content: space-between;
  padding: 16px 20px;
  border-bottom: 1px solid var(--color-border-subtle);
}

.npw__title {
  margin: 0;
  font-size: 15px;
  font-weight: 600;
  color: var(--color-text-primary);
}

.npw__steps {
  display: flex;
  gap: 6px;
}

.npw__step {
  display: inline-flex;
  align-items: center;
  justify-content: center;
  width: 22px;
  height: 22px;
  border-radius: 50%;
  font-size: 11px;
  font-weight: 600;
  background-color: var(--color-bg-tertiary, rgba(128, 128, 128, 0.15));
  color: var(--color-text-tertiary);
}

.npw__step--active {
  background-color: var(--color-primary);
  color: var(--color-on-primary, #fff);
}

.npw__step--done {
  background-color: color-mix(in srgb, var(--color-primary) 40%, transparent);
  color: var(--color-primary);
}

.npw__body {
  flex: 1;
  overflow-y: auto;
  padding: 20px;
}

.npw__hint {
  margin: 0 0 12px;
  font-size: 12px;
  color: var(--color-text-secondary);
}

.npw__hint--small {
  margin-top: 4px;
  font-size: 11px;
}

.npw__loading {
  padding: 24px;
  text-align: center;
  font-size: 12px;
  color: var(--color-text-tertiary);
}

.npw__cards {
  display: grid;
  grid-template-columns: repeat(auto-fill, minmax(160px, 1fr));
  gap: 10px;
}

.npw__card {
  display: flex;
  flex-direction: column;
  gap: 4px;
  padding: 12px;
  background: transparent;
  border: 1px solid var(--color-border-default);
  border-radius: var(--radius-sm);
  cursor: pointer;
  text-align: left;
  transition: border-color 80ms ease, background-color 80ms ease;
}

.npw__card:hover {
  border-color: var(--color-primary);
}

.npw__card--selected {
  border-color: var(--color-primary);
  background-color: color-mix(in srgb, var(--color-primary) 10%, transparent);
}

.npw__card-lang {
  font-size: 10px;
  font-weight: 600;
  text-transform: uppercase;
  letter-spacing: 0.04em;
  color: var(--color-text-tertiary);
}

.npw__card-name {
  font-size: 13px;
  font-weight: 600;
  color: var(--color-text-primary);
}

.npw__card-desc {
  font-size: 11px;
  color: var(--color-text-secondary);
  line-height: 1.4;
}

.npw__field {
  display: flex;
  flex-direction: column;
  gap: 4px;
  margin-bottom: 14px;
}

.npw__label {
  font-size: 12px;
  font-weight: 500;
  color: var(--color-text-secondary);
}

.npw__input {
  width: 100%;
  padding: 8px 10px;
  font-size: 13px;
  font-family: var(--font-sans);
  color: var(--color-text-primary);
  background-color: var(--color-bg-base, transparent);
  border: 1px solid var(--color-border-default);
  border-radius: var(--radius-sm);
  outline: none;
}

.npw__input:focus {
  border-color: var(--color-primary);
}

.npw__dir-row {
  display: flex;
  gap: 6px;
}

.npw__dir-row .npw__input {
  flex: 1;
}

.npw__browse {
  padding: 0 12px;
  font-size: 12px;
  background-color: var(--color-bg-tertiary, rgba(128, 128, 128, 0.12));
  border: 1px solid var(--color-border-default);
  border-radius: var(--radius-sm);
  color: var(--color-text-primary);
  cursor: pointer;
}

.npw__summary {
  margin: 0;
  display: flex;
  flex-direction: column;
  gap: 8px;
}

.npw__summary-row {
  display: flex;
  gap: 12px;
}

.npw__summary-row dt {
  flex: 0 0 120px;
  font-size: 12px;
  color: var(--color-text-tertiary);
}

.npw__summary-row dd {
  flex: 1;
  margin: 0;
  font-size: 12px;
  color: var(--color-text-primary);
  font-family: var(--font-mono);
  word-break: break-all;
}

.npw__success {
  margin-top: 14px;
  padding: 10px;
  background-color: color-mix(in srgb, var(--color-primary) 8%, transparent);
  border-radius: var(--radius-sm);
  font-size: 12px;
  color: var(--color-text-primary);
}

.npw__path {
  display: block;
  margin-top: 4px;
  font-family: var(--font-mono);
  font-size: 11px;
  word-break: break-all;
}

.npw__error {
  margin-top: 10px;
  padding: 8px 10px;
  background-color: color-mix(in srgb, #e53935 10%, transparent);
  border-radius: var(--radius-sm);
  font-size: 12px;
  color: #e53935;
}

.npw__footer {
  display: flex;
  justify-content: flex-end;
  gap: 8px;
  padding: 12px 20px;
  border-top: 1px solid var(--color-border-subtle);
}

.npw__btn {
  padding: 6px 14px;
  font-size: 12px;
  border-radius: var(--radius-sm);
  border: 1px solid var(--color-border-default);
  background-color: transparent;
  color: var(--color-text-primary);
  cursor: pointer;
}

.npw__btn:disabled {
  opacity: 0.5;
  cursor: not-allowed;
}

.npw__btn--ghost {
  margin-right: auto;
}

.npw__btn--primary {
  background-color: var(--color-primary);
  color: var(--color-on-primary, #fff);
  border-color: var(--color-primary);
}

.npw-fade-enter-active,
.npw-fade-leave-active {
  transition: opacity 120ms ease;
}

.npw-fade-enter-from,
.npw-fade-leave-to {
  opacity: 0;
}
</style>
