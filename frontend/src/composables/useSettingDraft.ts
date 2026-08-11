// Koyori IDE 模块 · Use Setting Draft。
// 喵，这是 Koyori IDE 的 Use Setting Draft 模块（前端实现）~
import { ref, onMounted } from "vue";
import type { Ref } from "vue";

/**
 * useSettingDraft - 通用设置区域草稿管理组合式函数
 *
 * 适用于 settings/ai/ 目录下 12+ 个设置区域组件中重复的
 * load → draft → save → reset 模式。
 *
 * @param loadFn 从后端加载当前设置的异步函数
 * @param saveFn 将草稿保存到后端的异步函数
 * @returns draft（响应式草稿）、loading、save、reset、isDirty
 */
export function useSettingDraft<T>(
  loadFn: () => Promise<T>,
  saveFn: (draft: T) => Promise<void>,
) {
  const draft = ref<T | null>(null) as Ref<T | null>;
  const loading = ref(false);
  const saving = ref(false);
  const error = ref<string | null>(null);
  const original = ref<T | null>(null) as Ref<T | null>;

  const isDirty = ref(false);

  async function load(): Promise<void> {
    loading.value = true;
    error.value = null;
    try {
      const data = await loadFn();
      draft.value = structuredClone(data);
      original.value = structuredClone(data);
      isDirty.value = false;
    } catch (e: unknown) {
      error.value = e instanceof Error ? e.message : String(e);
      console.error("useSettingDraft load failed:", e);
    } finally {
      loading.value = false;
    }
  }

  async function save(): Promise<void> {
    if (!draft.value) return;
    saving.value = true;
    error.value = null;
    try {
      await saveFn(structuredClone(draft.value));
      original.value = structuredClone(draft.value);
      isDirty.value = false;
    } catch (e: unknown) {
      error.value = e instanceof Error ? e.message : String(e);
      console.error("useSettingDraft save failed:", e);
    } finally {
      saving.value = false;
    }
  }

  function reset(): void {
    if (original.value) {
      draft.value = structuredClone(original.value);
      isDirty.value = false;
    }
  }

  function markDirty(): void {
    isDirty.value = true;
  }

  onMounted(() => {
    void load();
  });

  return {
    draft,
    original,
    loading,
    saving,
    error,
    isDirty,
    load,
    save,
    reset,
    markDirty,
  };
}
