<script setup lang="ts">
import { onMounted, ref } from "vue";
import {
  Delete,
  Download,
  Refresh,
  RefreshLeft,
  VideoPause,
  VideoPlay,
} from "@element-plus/icons-vue";
import { ElMessage } from "element-plus";
import {
  languagePackService,
  type LanguagePackInfo,
} from "@/api/languagePacks";
import { useI18n } from "@/lib/i18n";

const { t } = useI18n();
const packs = ref<LanguagePackInfo[]>([]);
const loading = ref(false);
const busyID = ref("");

async function refreshPacks(): Promise<void> {
  loading.value = true;
  try {
    const [inventory] = await Promise.all([
      languagePackService.list(),
      languagePackService.refreshRuntime(),
    ]);
    packs.value = inventory;
  } catch (error) {
    ElMessage.error(
      `${t("languagePacks.loadFailed")}: ${error instanceof Error ? error.message : String(error)}`,
    );
  } finally {
    loading.value = false;
  }
}

async function installPack(): Promise<void> {
  try {
    await languagePackService.install();
    await refreshPacks();
    ElMessage.success(t("languagePacks.installed"));
  } catch (error) {
    ElMessage.error(error instanceof Error ? error.message : String(error));
  }
}

async function changePack(
  pack: LanguagePackInfo,
  action: "disable" | "enable" | "rollback" | "uninstall",
): Promise<void> {
  busyID.value = pack.id;
  try {
    await languagePackService[action](pack.id);
    await refreshPacks();
  } catch (error) {
    ElMessage.error(error instanceof Error ? error.message : String(error));
  } finally {
    busyID.value = "";
  }
}

onMounted(() => {
  void refreshPacks();
});
</script>

<template>
  <section class="settings-section language-packs-section">
    <div class="section-heading">
      <h2 class="section-title">{{ t("settings.languagePacks") }}</h2>
      <div class="section-actions">
        <el-button
          :loading="loading"
          :aria-label="t('languagePacks.refresh')"
          :title="t('languagePacks.refresh')"
          @click="refreshPacks"
        >
          <el-icon><Refresh /></el-icon>
        </el-button>
        <el-button
          type="primary"
          :aria-label="t('languagePacks.install')"
          @click="installPack"
        >
          <el-icon><Download /></el-icon
          ><span>{{ t("languagePacks.install") }}</span>
        </el-button>
      </div>
    </div>

    <p v-if="!loading && packs.length === 0" class="language-packs-empty">
      {{ t("languagePacks.empty") }}
    </p>
    <div
      v-for="pack in packs"
      :key="`${pack.id}@${pack.version}`"
      class="language-pack-row"
    >
      <div class="language-pack-main">
        <strong>{{ pack.displayName }}</strong>
        <span class="language-pack-meta"
          >{{ pack.id }} @ {{ pack.version }}</span
        >
        <span class="language-pack-meta">{{
          (pack.languages ?? []).join(", ")
        }}</span>
      </div>
      <div class="language-pack-actions">
        <el-tag v-if="pack.builtIn" size="small">{{
          t("languagePacks.builtIn")
        }}</el-tag>
        <el-tag
          v-else-if="pack.active && pack.enabled"
          type="success"
          size="small"
          >{{ t("languagePacks.active") }}</el-tag
        >
        <el-tag v-else type="info" size="small">{{
          t("languagePacks.disabled")
        }}</el-tag>
        <template v-if="!pack.builtIn && pack.active">
          <el-button
            v-if="pack.enabled"
            text
            :loading="busyID === pack.id"
            :aria-label="t('languagePacks.disable')"
            :title="t('languagePacks.disable')"
            @click="changePack(pack, 'disable')"
          >
            <el-icon><VideoPause /></el-icon>
          </el-button>
          <el-button
            v-else
            text
            :loading="busyID === pack.id"
            :aria-label="t('languagePacks.enable')"
            :title="t('languagePacks.enable')"
            @click="changePack(pack, 'enable')"
          >
            <el-icon><VideoPlay /></el-icon>
          </el-button>
          <el-button
            text
            :loading="busyID === pack.id"
            :aria-label="t('languagePacks.rollback')"
            :title="t('languagePacks.rollback')"
            @click="changePack(pack, 'rollback')"
          >
            <el-icon><RefreshLeft /></el-icon>
          </el-button>
          <el-button
            text
            type="danger"
            :loading="busyID === pack.id"
            :aria-label="t('languagePacks.uninstall')"
            :title="t('languagePacks.uninstall')"
            @click="changePack(pack, 'uninstall')"
          >
            <el-icon><Delete /></el-icon>
          </el-button>
        </template>
      </div>
    </div>
  </section>
</template>

<style scoped>
.section-heading,
.language-pack-row,
.language-pack-actions,
.section-actions {
  display: flex;
  align-items: center;
}
.section-heading,
.language-pack-row {
  justify-content: space-between;
  gap: 12px;
}
.section-heading {
  margin-bottom: 12px;
}
.section-actions,
.language-pack-actions {
  gap: 6px;
}
.language-pack-row {
  min-height: 58px;
  padding: 9px 0;
  border-top: 1px solid var(--el-border-color-lighter);
}
.language-pack-main {
  min-width: 0;
  display: grid;
  gap: 3px;
}
.language-pack-meta {
  color: var(--el-text-color-secondary);
  font-size: 12px;
  overflow-wrap: anywhere;
}
.language-packs-empty {
  color: var(--el-text-color-secondary);
}
@media (max-width: 640px) {
  .language-pack-row {
    align-items: flex-start;
    flex-direction: column;
  }
  .language-pack-actions {
    width: 100%;
    justify-content: flex-end;
  }
}
</style>
