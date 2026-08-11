<script setup lang="ts">
// Koyori IDE 组件 · Marketplace Panel；交互服务：插件市场（MarketplaceService）。
// 喵，这是 Marketplace Panel，负责 Koyori IDE 的界面呈现喵~
// G-VSC-01 + G-MKT-02: VS Code extension marketplace panel.
//
// Surfaces the Open VSX Registry search/browse/install flow in the "extensions"
// activity tab. The panel has three regions:
//   1. A security warning banner reminding the user that installs are
//      disabled-by-default and SHA-256 verified (G-SEC-12 req. 2 & 3).
//   2. A search bar + results list (or the detail view for a selected hit).
//      Includes a category dropdown and featured extensions landing page.
//   3. An installed-extensions list with enable/disable + uninstall controls.
//
// The panel is self-contained: it calls marketplaceService directly and can be
// mounted anywhere (the SidePanel "extensions" tab sub-view, or a route).
import { computed, onMounted, reactive, ref } from "vue";
import { ElMessage } from "element-plus";
import { ArrowLeft, Delete, Download, Loading, Refresh, Search } from "@element-plus/icons-vue";
import { marketplaceService } from "@/api/services";
import { errorMessage } from "@/lib/errors";
import ExtensionPermissionDialog from "@/components/modals/ExtensionPermissionDialog.vue";
import MarkdownContent from "@/components/common/MarkdownContent.vue";
import {
  requestEnableExtension,
  pendingApproval,
  dismissPermissionDialog,
} from "@/stores/extensionSecurity";
import { useI18n } from "@/lib/i18n";
import { renderMarketplaceMarkdown } from "@/lib/markdown";
import { deactivateExtension } from "@/lib/vscodeExtensionActivation";
import type {
  ExtensionDetail,
  ExtensionSearchResult,
  ExtensionUpdate,
  InstalledExtension,
} from "@/types";

const { t } = useI18n();

// --- search state ---
const query = ref("");
const results = ref<ExtensionSearchResult[]>([]);
const searching = ref(false);
const hasSearched = ref(false);

// M-26: 分页 — 一次性获取较大批次，前端按 visibleCount 分批展示。
// "加载更多"按钮每次增加 30 条；新搜索时重置为 30。
const PAGE_SIZE = 200; // 后端单次返回的最大条数（确保拿到全部结果）
const VISIBLE_INCREMENT = 30;
const visibleCount = ref(VISIBLE_INCREMENT);
const visibleResults = computed(() => results.value.slice(0, visibleCount.value));
const hasMore = computed(() => visibleCount.value < results.value.length);

function loadMore(): void {
  visibleCount.value += VISIBLE_INCREMENT;
}

// G-MKT-02: category filter and featured extensions
const categories = ref<string[]>([]);
const selectedCategory = ref("");
const featured = ref<ExtensionSearchResult[]>([]);
const loadingFeatured = ref(false);

// --- detail state ---
const detail = ref<ExtensionDetail | null>(null);
const loadingDetail = ref(false);
const readmeHtml = ref("");
const loadingReadme = ref(false);

// --- installed state ---
const installed = ref<InstalledExtension[]>([]);
const loadingInstalled = ref(false);
const updates = ref<ExtensionUpdate[]>([]);
const checkingUpdates = ref(false);

// --- install-in-progress tracking (keyed "publisher.name") ---
// M-20: Vue 3 `ref(new Set())` does NOT trigger reactivity on .add/.delete
// (Vue cannot intercept Set methods on a plain Set held by a ref). Use
// `reactive(new Set())` instead — Vue 3.4+ (we run 3.5.x) tracks Set/Map
// mutations on reactive collections, so .add/.delete re-render the UI.
const installing = reactive<Set<string>>(new Set());
function installKey(publisher: string, name: string): string {
  return `${publisher}.${name}`;
}
function isInstalling(publisher: string, name: string): boolean {
  return installing.has(installKey(publisher, name));
}

// The currently visible list region. "search" shows search results (or the
// featured/empty prompt before a search); "installed" shows the installed list.
const view = ref<"search" | "installed">("search");

const installedIds = computed(
  () => new Set(installed.value.map((e) => installKey(e.publisher, e.name))),
);

function isInstalled(publisher: string, name: string): boolean {
  return installedIds.value.has(installKey(publisher, name));
}

// G-MKT-02: Whether the featured landing should be shown (no search, no category).
const showFeatured = computed(
  () => !hasSearched.value && !selectedCategory.value && !searching.value,
);

// --- search ---
async function runSearch(): Promise<void> {
  const q = query.value.trim();
  if (!q) {
    results.value = [];
    hasSearched.value = false;
    return;
  }
  searching.value = true;
  hasSearched.value = true;
  selectedCategory.value = ""; // clear category filter when searching
  visibleCount.value = VISIBLE_INCREMENT; // M-26: 新搜索重置分页
  try {
    results.value = await marketplaceService.searchExtensions(q, 1, PAGE_SIZE);
  } catch (e: unknown) {
    ElMessage.error(errorMessage(e));
    results.value = [];
  } finally {
    searching.value = false;
  }
}

// G-MKT-02: Browse by category
async function browseCategory(cat: string): Promise<void> {
  if (!cat) {
    results.value = [];
    hasSearched.value = false;
    return;
  }
  searching.value = true;
  hasSearched.value = true;
  query.value = ""; // clear search query when browsing category
  visibleCount.value = VISIBLE_INCREMENT; // M-26: 新分类重置分页
  try {
    results.value = await marketplaceService.browseByCategory(cat, 1, PAGE_SIZE);
  } catch (e: unknown) {
    ElMessage.error(errorMessage(e));
    results.value = [];
  } finally {
    searching.value = false;
  }
}

// G-MKT-02: Load featured extensions for the landing page
async function loadFeatured(): Promise<void> {
  loadingFeatured.value = true;
  try {
    featured.value = await marketplaceService.getFeaturedExtensions();
  } catch {
    featured.value = [];
  } finally {
    loadingFeatured.value = false;
  }
}

// --- detail ---
async function openDetail(hit: ExtensionSearchResult): Promise<void> {
  loadingDetail.value = true;
  detail.value = null;
  readmeHtml.value = "";
  try {
    detail.value = await marketplaceService.getExtensionDetail(hit.publisher, hit.name);
    // G-MKT-02: Fetch README asynchronously (don't block detail display).
    loadReadme(hit.publisher, hit.name);
  } catch (e: unknown) {
    ElMessage.error(errorMessage(e));
  } finally {
    loadingDetail.value = false;
  }
}

// G-MKT-02: Fetch and render README markdown
async function loadReadme(publisher: string, name: string): Promise<void> {
  loadingReadme.value = true;
  readmeHtml.value = "";
  try {
    const md = await marketplaceService.getExtensionReadme(publisher, name);
    if (md) {
      // H-16: 第三方扩展 README 使用更严格的白名单净化，防止 v-html 渲染时
      // 的 XSS / DOM clobbering / CSS 注入。
      readmeHtml.value = renderMarketplaceMarkdown(md);
    }
  } catch {
    // README is optional - fail silently.
  } finally {
    loadingReadme.value = false;
  }
}

function closeDetail(): void {
  detail.value = null;
  readmeHtml.value = "";
}

// --- install / uninstall ---
async function install(publisher: string, name: string, version: string): Promise<void> {
  const key = installKey(publisher, name);
  if (installing.has(key)) return;
  installing.add(key);
  try {
    await marketplaceService.downloadAndInstallExtension(publisher, name, version);
    ElMessage.success(t("marketplace.installSuccess", { id: key }));
    await refreshMarketplaceState();
  } catch (e: unknown) {
    ElMessage.error(errorMessage(e));
  } finally {
    installing.delete(key);
  }
}

function warnDeactivationFailure(extensionId: string, error: unknown): void {
  ElMessage.warning(
    t("marketplace.deactivationWarning", {
      id: extensionId,
      error: errorMessage(error),
    }),
  );
}

async function update(ext: InstalledExtension, version: string): Promise<void> {
  const extensionId = installKey(ext.publisher, ext.name);
  if (installing.has(extensionId)) return;
  installing.add(extensionId);
  try {
    await marketplaceService.updateExtension(ext.publisher, ext.name, version);
    ElMessage.success(t("marketplace.updateSuccess", { id: extensionId, version }));
    await refreshMarketplaceState();
  } catch (error) {
    ElMessage.error(errorMessage(error));
  } finally {
    installing.delete(extensionId);
  }
}

async function uninstall(ext: InstalledExtension): Promise<void> {
  const extensionId = installKey(ext.publisher, ext.name);
  try {
    await marketplaceService.uninstallExtension(ext.publisher, ext.name);
    ElMessage.success(t("marketplace.uninstallSuccess", { id: extensionId }));
    await refreshMarketplaceState();
  } catch (e: unknown) {
    ElMessage.error(errorMessage(e));
  }
}

async function toggleEnabled(ext: InstalledExtension, enabled: boolean): Promise<void> {
  const extensionId = `${ext.publisher}.${ext.name}`;
  if (enabled) {
    const ok = await requestEnableExtension(extensionId, true);
    if (!ok) {
      ext.enabled = false;
      return;
    }
    try {
      await marketplaceService.setExtensionEnabled(ext.publisher, ext.name, true);
    } catch (e: unknown) {
      ElMessage.error(errorMessage(e));
      ext.enabled = false;
      return;
    }
    ext.enabled = true;
  } else {
    try {
      await marketplaceService.setExtensionEnabled(ext.publisher, ext.name, false);
      try {
        await deactivateExtension(extensionId);
      } catch (error) {
        warnDeactivationFailure(extensionId, error);
      }
      ext.enabled = false;
    } catch (e: unknown) {
      ElMessage.error(errorMessage(e));
      ext.enabled = true;
    }
  }
}

// Handle user approval from the ExtensionPermissionDialog for restricted
// extensions. After the security backend enables it, persist to the
// marketplace state file so the extension stays enabled after restart.
async function handleApprove(extensionId: string): Promise<void> {
  // Parse "publisher.name" from extensionId.
  const dotIdx = extensionId.indexOf(".");
  if (dotIdx > 0) {
    const publisher = extensionId.slice(0, dotIdx);
    const name = extensionId.slice(dotIdx + 1);
    try {
      await marketplaceService.setExtensionEnabled(publisher, name, true, true);
      dismissPermissionDialog();
    } catch (e: unknown) {
      ElMessage.error(errorMessage(e));
      return;
    }
  }
  await refreshInstalled();
}

async function refreshInstalled(): Promise<void> {
  loadingInstalled.value = true;
  try {
    installed.value = await marketplaceService.listInstalledExtensions();
  } catch (e: unknown) {
    ElMessage.error(errorMessage(e));
    installed.value = [];
  } finally {
    loadingInstalled.value = false;
  }
}

async function refreshUpdateList(): Promise<void> {
  try {
    updates.value = await marketplaceService.checkForUpdates();
  } catch (error) {
    console.warn("[marketplace] failed to refresh extension updates:", error);
  }
}

async function refreshMarketplaceState(): Promise<void> {
  await Promise.all([refreshInstalled(), refreshUpdateList()]);
}

// G-MKT-02: Check for updates on installed extensions
async function checkForUpdates(): Promise<void> {
  checkingUpdates.value = true;
  try {
    updates.value = await marketplaceService.checkForUpdates();
    if (updates.value.length === 0) {
      ElMessage.success(t("marketplace.upToDate"));
    } else {
      ElMessage.info(t("marketplace.updatesAvailable", { count: updates.value.length }));
    }
  } catch (e: unknown) {
    ElMessage.error(errorMessage(e));
  } finally {
    checkingUpdates.value = false;
  }
}

// G-MKT-02: Check if an installed extension has an update available
function getUpdateFor(publisher: string, name: string): ExtensionUpdate | undefined {
  return updates.value.find((u) => u.publisher === publisher && u.name === name);
}

function formatCount(n: number): string {
  if (n >= 1_000_000) return `${(n / 1_000_000).toFixed(1)}M`;
  if (n >= 1_000) return `${(n / 1_000).toFixed(1)}K`;
  return String(n);
}

onMounted(async () => {
  void refreshInstalled();
  void loadFeatured();
  try {
    categories.value = await marketplaceService.getCategories();
  } catch {
    categories.value = [];
  }
});
</script>

<template>
  <div class="marketplace">
    <!-- Security warning banner (G-SEC-12 req. 2 & 3) -->
    <div class="marketplace__security" role="note">
      <span class="marketplace__security-title">{{ t("marketplace.securityTitle") }}</span>
      <p class="marketplace__security-text">{{ t("marketplace.securityText") }}</p>
    </div>

    <!-- Search bar + category filter (G-MKT-02) -->
    <div class="marketplace__search">
      <el-input
        v-model="query"
        :placeholder="t('marketplace.searchPlaceholder')"
        clearable
        :aria-label="t('marketplace.searchPlaceholder')"
        @keyup.enter="runSearch"
      >
        <template #prefix>
          <el-icon><Search /></el-icon>
        </template>
      </el-input>
      <el-select
        v-model="selectedCategory"
        :placeholder="t('marketplace.categoryFilter')"
        clearable
        size="default"
        style="width: 180px"
        :aria-label="t('marketplace.categoryFilter')"
        @change="browseCategory"
      >
        <el-option
          v-for="cat in categories"
          :key="cat"
          :label="cat"
          :value="cat"
        />
      </el-select>
      <el-button
        type="primary"
        :loading="searching"
        :aria-label="t('marketplace.searchButton')"
        @click="runSearch"
      >
        {{ t("marketplace.searchButton") }}
      </el-button>
    </div>

    <!-- Sub-tab toggle between search results and installed list -->
    <div class="marketplace__tabs" role="tablist">
      <button
        type="button"
        role="tab"
        :aria-selected="view === 'search'"
        class="marketplace__tab"
        :class="{ 'marketplace__tab--active': view === 'search' && !detail }"
        @click="view = 'search'"
      >
        {{ t("marketplace.tabResults") }}
      </button>
      <button
        type="button"
        role="tab"
        :aria-selected="view === 'installed'"
        class="marketplace__tab"
        :class="{ 'marketplace__tab--active': view === 'installed' && !detail }"
        @click="view = 'installed'"
      >
        {{ t("marketplace.tabInstalled") }} ({{ installed.length }})
      </button>
    </div>

    <div class="marketplace__body">
      <!-- Detail view (overlays the active list) -->
      <div v-if="detail || loadingDetail" key="detail" class="marketplace__detail">
        <button type="button" class="marketplace__back" @click="closeDetail">
          <el-icon><ArrowLeft /></el-icon>
          <span>{{ t("marketplace.backToList") }}</span>
        </button>
        <div v-if="loadingDetail" class="marketplace__loading">
          <el-icon class="is-loading"><Loading /></el-icon>
          <span>{{ t("marketplace.loading") }}</span>
        </div>
        <article v-else-if="detail" class="marketplace__detail-content">
          <header class="marketplace__detail-header">
            <div class="marketplace__detail-titles">
              <h3 class="marketplace__detail-name">{{ detail.displayName || detail.name }}</h3>
              <span class="marketplace__detail-id">{{ detail.publisher }}.{{ detail.name }}</span>
            </div>
            <el-button
              v-if="!isInstalled(detail.publisher, detail.name)"
              type="primary"
              :loading="isInstalling(detail.publisher, detail.name)"
              @click="install(detail.publisher, detail.name, detail.version)"
            >
              <el-icon><Download /></el-icon>
              <span>{{ t("marketplace.install") }}</span>
            </el-button>
            <el-tag v-else type="success" size="small">
              {{ t("marketplace.installed") }}
            </el-tag>
          </header>

          <p v-if="detail.description" class="marketplace__detail-desc">{{ detail.description }}</p>

          <dl class="marketplace__meta">
            <div v-if="detail.version" class="marketplace__meta-row">
              <dt>{{ t("marketplace.metaVersion") }}</dt>
              <dd>{{ detail.version }}</dd>
            </div>
            <div v-if="detail.license" class="marketplace__meta-row">
              <dt>{{ t("marketplace.metaLicense") }}</dt>
              <dd>{{ detail.license }}</dd>
            </div>
            <div class="marketplace__meta-row">
              <dt>{{ t("marketplace.metaDownloads") }}</dt>
              <dd>{{ formatCount(detail.downloadCount) }}</dd>
            </div>
            <div v-if="detail.ratingCount > 0" class="marketplace__meta-row">
              <dt>{{ t("marketplace.metaRating") }}</dt>
              <dd>{{ detail.rating.toFixed(1) }} ({{ detail.ratingCount }})</dd>
            </div>
            <div v-if="detail.repository" class="marketplace__meta-row">
              <dt>{{ t("marketplace.metaRepository") }}</dt>
              <dd class="marketplace__link">{{ detail.repository }}</dd>
            </div>
          </dl>

          <div v-if="detail.categories && detail.categories.length" class="marketplace__tags">
            <el-tag v-for="c in detail.categories" :key="c" size="small" type="info">{{ c }}</el-tag>
          </div>

          <div v-if="detail.versions && detail.versions.length" class="marketplace__versions">
            <h4 class="marketplace__versions-title">{{ t("marketplace.versions") }}</h4>
            <ul class="marketplace__versions-list">
              <li v-for="v in detail.versions" :key="v.version" class="marketplace__version-item">
                <span class="marketplace__version-num">{{ v.version }}</span>
                <span v-if="v.date" class="marketplace__version-date">{{ v.date }}</span>
              </li>
            </ul>
          </div>

          <!-- G-MKT-02: README section -->
          <div v-if="loadingReadme" class="marketplace__readme-loading">
            <el-icon class="is-loading"><Loading /></el-icon>
            <span>{{ t("marketplace.loading") }}</span>
          </div>
          <MarkdownContent
            v-else-if="readmeHtml"
            class="marketplace__readme"
            :html="readmeHtml"
          />
        </article>
      </div>

      <!-- Search results -->
      <div v-else-if="view === 'search'" key="search" class="marketplace__results">
        <!-- G-MKT-02: Featured extensions landing page -->
        <div v-if="showFeatured" class="marketplace__featured">
          <h4 class="marketplace__featured-title">{{ t("marketplace.featured") }}</h4>
          <div v-if="loadingFeatured" class="marketplace__loading">
            <el-icon class="is-loading"><Loading /></el-icon>
            <span>{{ t("marketplace.loading") }}</span>
          </div>
          <ul v-else-if="featured.length > 0" class="marketplace__list">
            <li
              v-for="r in featured"
              :key="r.id"
              class="marketplace__item"
            >
              <button type="button" class="marketplace__item-main" @click="openDetail(r)">
                <div class="marketplace__item-title">
                  <span class="marketplace__name">{{ r.displayName || r.name }}</span>
                  <span class="marketplace__version">v{{ r.version }}</span>
                </div>
                <p class="marketplace__item-publisher">{{ t("marketplace.by", { author: r.publisher }) }}</p>
                <p v-if="r.description" class="marketplace__item-desc">{{ r.description }}</p>
                <div class="marketplace__item-stats">
                  <span v-if="r.downloadCount > 0">{{ t("marketplace.metaDownloads") }}: {{ formatCount(r.downloadCount) }}</span>
                  <span v-if="r.ratingCount > 0">{{ r.rating.toFixed(1) }} rating ({{ r.ratingCount }})</span>
                </div>
              </button>
              <div class="marketplace__item-actions">
                <el-button
                  v-if="!isInstalled(r.publisher, r.name)"
                  size="small"
                  type="primary"
                  :loading="isInstalling(r.publisher, r.name)"
                  @click="install(r.publisher, r.name, r.version)"
                >
                  {{ t("marketplace.install") }}
                </el-button>
                <el-tag v-else type="success" size="small">
                  {{ t("marketplace.installed") }}
                </el-tag>
              </div>
            </li>
          </ul>
          <div v-else class="marketplace__empty">
            {{ t("marketplace.searchPrompt") }}
          </div>
        </div>

        <div v-else-if="searching" class="marketplace__loading">
          <el-icon class="is-loading"><Loading /></el-icon>
          <span>{{ t("marketplace.loading") }}</span>
        </div>
        <div v-else-if="results.length === 0" class="marketplace__empty">
          {{ hasSearched ? t("marketplace.noResults") : t("marketplace.searchPrompt") }}
        </div>
        <ul v-else class="marketplace__list">
          <li
            v-for="r in visibleResults"
            :key="r.id"
            class="marketplace__item"
          >
            <button type="button" class="marketplace__item-main" @click="openDetail(r)">
              <div class="marketplace__item-title">
                <span class="marketplace__name">{{ r.displayName || r.name }}</span>
                <span class="marketplace__version">v{{ r.version }}</span>
              </div>
              <p class="marketplace__item-publisher">{{ t("marketplace.by", { author: r.publisher }) }}</p>
              <p v-if="r.description" class="marketplace__item-desc">{{ r.description }}</p>
              <div class="marketplace__item-stats">
                <span v-if="r.downloadCount > 0">{{ t("marketplace.metaDownloads") }}: {{ formatCount(r.downloadCount) }}</span>
                <span v-if="r.ratingCount > 0">{{ r.rating.toFixed(1) }} rating ({{ r.ratingCount }})</span>
              </div>
            </button>
            <div class="marketplace__item-actions">
              <el-button
                v-if="!isInstalled(r.publisher, r.name)"
                size="small"
                type="primary"
                :loading="isInstalling(r.publisher, r.name)"
                @click="install(r.publisher, r.name, r.version)"
              >
                {{ t("marketplace.install") }}
              </el-button>
              <el-tag v-else type="success" size="small">
                {{ t("marketplace.installed") }}
              </el-tag>
            </div>
          </li>
        </ul>
        <!-- M-26: 加载更多按钮 — 当还有未展示的结果时显示 -->
        <div v-if="hasMore" class="marketplace__load-more">
          <el-button :loading="searching" @click="loadMore">
            {{ t("marketplace.loadMore") }}
          </el-button>
        </div>
      </div>

      <!-- Installed extensions -->
      <div v-else key="installed" class="marketplace__installed">
        <!-- G-MKT-02: Check for updates button -->
        <div v-if="installed.length > 0" class="marketplace__updates-bar">
          <el-button
            size="small"
            :loading="checkingUpdates"
            :aria-label="t('marketplace.checkUpdates')"
            @click="checkForUpdates"
          >
            <el-icon><Refresh /></el-icon>
            <span>{{ t("marketplace.checkUpdates") }}</span>
          </el-button>
          <el-tag v-if="updates.length > 0" type="warning" size="small">
            {{ t("marketplace.updatesAvailable", { count: updates.length }) }}
          </el-tag>
        </div>
        <div v-if="loadingInstalled" class="marketplace__loading">
          <el-icon class="is-loading"><Loading /></el-icon>
          <span>{{ t("marketplace.loading") }}</span>
        </div>
        <div v-else-if="installed.length === 0" class="marketplace__empty">
          {{ t("marketplace.noInstalled") }}
        </div>
        <ul v-else class="marketplace__list">
          <li
            v-for="ext in installed"
            :key="`${ext.publisher}.${ext.name}`"
            class="marketplace__item"
            :class="{ 'is-disabled': !ext.enabled }"
          >
            <div class="marketplace__item-main">
              <div class="marketplace__item-title">
                <span class="marketplace__name">{{ ext.publisher }}.{{ ext.name }}</span>
                <span class="marketplace__version">v{{ ext.version }}</span>
                <el-tag size="small" :type="ext.enabled ? 'success' : 'info'">
                  {{ ext.enabled ? t("marketplace.enabled") : t("marketplace.disabled") }}
                </el-tag>
                <el-tag
                  v-if="getUpdateFor(ext.publisher, ext.name)"
                  size="small"
                  type="warning"
                  class="marketplace__update-tag"
                >
                  {{ t("marketplace.updateAvailable", { version: getUpdateFor(ext.publisher, ext.name)!.latestVersion }) }}
                </el-tag>
              </div>
              <p class="marketplace__item-desc marketplace__item-desc--muted">
                {{ ext.enabled ? t("marketplace.enabledHint") : t("marketplace.disabledHint") }}
              </p>
            </div>
            <div class="marketplace__item-actions">
              <el-button
                v-if="getUpdateFor(ext.publisher, ext.name)"
                size="small"
                type="primary"
                plain
                :loading="isInstalling(ext.publisher, ext.name)"
                :aria-label="t('marketplace.updateAvailable', { version: getUpdateFor(ext.publisher, ext.name)!.latestVersion })"
                @click="update(ext, getUpdateFor(ext.publisher, ext.name)!.latestVersion)"
              >
                <el-icon><Download /></el-icon>
              </el-button>
              <el-switch
                :model-value="ext.enabled"
                :aria-label="t('marketplace.enableDisableAria', { id: `${ext.publisher}.${ext.name}` })"
                @change="(val: boolean) => toggleEnabled(ext, val)"
              />
              <el-button
                size="small"
                type="danger"
                plain
                :aria-label="t('marketplace.uninstallAria', { id: `${ext.publisher}.${ext.name}` })"
                @click="uninstall(ext)"
              >
                <el-icon><Delete /></el-icon>
              </el-button>
            </div>
          </li>
        </ul>
      </div>
    </div>
    <!-- G-VSC-03: Permission approval dialog for Restricted extensions -->
    <ExtensionPermissionDialog
      :visible="!!pendingApproval"
      :info="pendingApproval"
      @approve="handleApprove"
      @close="dismissPermissionDialog"
    />
  </div>
</template>

<style scoped>
.marketplace {
  display: flex;
  flex-direction: column;
  gap: 10px;
  height: 100%;
  overflow: hidden;
  padding: 10px 8px;
}

/* Security warning banner - visually distinct (amber-ish) so the user notices
   the default-disabled + SHA-256 policy before installing anything. */
.marketplace__security {
  display: flex;
  flex-direction: column;
  gap: 2px;
  padding: 8px 10px;
  border: 1px solid color-mix(in srgb, var(--color-warning, #e6a23c) 45%, transparent);
  border-radius: var(--radius-md);
  background: color-mix(in srgb, var(--color-warning, #e6a23c) 10%, transparent);
}

.marketplace__security-title {
  font-size: 0.75rem;
  font-weight: 600;
  color: var(--color-warning, #e6a23c);
  letter-spacing: 0.01em;
}

.marketplace__security-text {
  margin: 0;
  font-size: 0.75rem;
  line-height: 1.45;
  color: var(--color-text-secondary, #a0a0a0);
}

.marketplace__search {
  display: flex;
  gap: 6px;
  flex-shrink: 0;
}

.marketplace__tabs {
  display: flex;
  gap: 4px;
  border-bottom: 1px solid var(--color-border-subtle, rgba(255, 255, 255, 0.08));
  flex-shrink: 0;
}

.marketplace__tab {
  padding: 6px 10px;
  border: none;
  background: transparent;
  color: var(--color-text-tertiary, #707070);
  font-size: 0.8125rem;
  font-weight: 500;
  cursor: pointer;
  border-bottom: 2px solid transparent;
  transition: color var(--transition-fast, 150ms) ease, border-color var(--transition-fast, 150ms) ease;
}

.marketplace__tab:hover {
  color: var(--color-text-secondary, #a0a0a0);
}

.marketplace__tab--active {
  color: var(--chrome-text-active, var(--color-primary, #4285f4));
  border-bottom-color: var(--chrome-text-active, var(--color-primary, #4285f4));
}

.marketplace__body {
  flex: 1;
  overflow-y: auto;
  min-height: 0;
}

.marketplace__loading,
.marketplace__empty {
  display: flex;
  align-items: center;
  justify-content: center;
  gap: 6px;
  padding: 24px 12px;
  font-size: 0.8125rem;
  color: var(--color-text-tertiary, #707070);
  text-align: center;
}

.marketplace__list {
  list-style: none;
  margin: 0;
  padding: 0;
  display: flex;
  flex-direction: column;
  gap: 8px;
}

/* M-26: 加载更多按钮容器 */
.marketplace__load-more {
  display: flex;
  justify-content: center;
  padding: 12px 0 4px;
}

.marketplace__item {
  display: flex;
  align-items: flex-start;
  justify-content: space-between;
  gap: 10px;
  padding: 10px 12px;
  border: 1px solid var(--color-border-default, #2a2a2c);
  border-radius: var(--radius-md);
  background: var(--color-bg-surface-container-low, #161616);
  transition: border-color var(--transition-fast, 150ms) ease;
}

.marketplace__item:hover {
  border-color: var(--color-primary, #a0c4ff);
}

.marketplace__item.is-disabled {
  opacity: 0.7;
}

.marketplace__item-main {
  flex: 1;
  min-width: 0;
  display: flex;
  flex-direction: column;
  gap: 3px;
  text-align: left;
  background: transparent;
  border: none;
  padding: 0;
  color: inherit;
  cursor: pointer;
  font: inherit;
}

.marketplace__item-title {
  display: flex;
  align-items: center;
  gap: 6px;
  flex-wrap: wrap;
}

.marketplace__name {
  font-size: 0.875rem;
  font-weight: 600;
  color: var(--color-text-primary, #f0f0f0);
}

.marketplace__version {
  font-family: var(--font-mono);
  font-size: 0.75rem;
  color: var(--color-text-secondary, #a0a0a0);
}

.marketplace__item-publisher,
.marketplace__item-desc {
  margin: 0;
  font-size: 0.8125rem;
  color: var(--color-text-secondary, #a0a0a0);
  line-height: 1.45;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.marketplace__item-desc--muted {
  font-style: italic;
}

.marketplace__item-stats {
  display: flex;
  gap: 10px;
  font-size: 0.7rem;
  color: var(--color-text-tertiary, #707070);
}

.marketplace__item-actions {
  display: flex;
  align-items: center;
  gap: 8px;
  flex-shrink: 0;
}

/* --- detail view --- */
.marketplace__detail {
  display: flex;
  flex-direction: column;
  gap: 10px;
  padding: 4px 4px 12px;
}

.marketplace__back {
  display: inline-flex;
  align-items: center;
  gap: 4px;
  align-self: flex-start;
  padding: 4px 8px;
  border: none;
  background: transparent;
  color: var(--color-text-secondary, #a0a0a0);
  font-size: 0.8125rem;
  cursor: pointer;
  border-radius: var(--radius-sm);
  transition: background-color var(--transition-fast, 150ms) ease;
}

.marketplace__back:hover {
  background-color: var(--chrome-hover-bg, rgba(255, 255, 255, 0.06));
}

.marketplace__detail-content {
  display: flex;
  flex-direction: column;
  gap: 10px;
}

.marketplace__detail-header {
  display: flex;
  align-items: flex-start;
  justify-content: space-between;
  gap: 10px;
}

.marketplace__detail-titles {
  display: flex;
  flex-direction: column;
  gap: 2px;
  min-width: 0;
}

.marketplace__detail-name {
  margin: 0;
  font-size: 1rem;
  font-weight: 600;
  color: var(--color-text-primary, #f0f0f0);
}

.marketplace__detail-id {
  font-family: var(--font-mono);
  font-size: 0.75rem;
  color: var(--color-text-tertiary, #707070);
}

.marketplace__detail-desc {
  margin: 0;
  font-size: 0.8125rem;
  color: var(--color-text-secondary, #a0a0a0);
  line-height: 1.5;
}

.marketplace__meta {
  margin: 0;
  display: grid;
  grid-template-columns: 1fr;
  gap: 4px;
}

.marketplace__meta-row {
  display: flex;
  gap: 8px;
  font-size: 0.8125rem;
}

.marketplace__meta-row dt {
  min-width: 90px;
  color: var(--color-text-tertiary, #707070);
  font-weight: 500;
}

.marketplace__meta-row dd {
  margin: 0;
  color: var(--color-text-secondary, #a0a0a0);
  word-break: break-all;
}

.marketplace__link {
  color: var(--color-primary, #4285f4);
}

.marketplace__tags {
  display: flex;
  flex-wrap: wrap;
  gap: 4px;
}

.marketplace__versions-title {
  margin: 4px 0 4px;
  font-size: 0.8125rem;
  font-weight: 600;
  color: var(--color-text-secondary, #a0a0a0);
}

.marketplace__versions-list {
  list-style: none;
  margin: 0;
  padding: 0;
  display: flex;
  flex-direction: column;
  gap: 3px;
}

.marketplace__version-item {
  display: flex;
  justify-content: space-between;
  font-size: 0.75rem;
  color: var(--color-text-tertiary, #707070);
  padding: 2px 0;
}

.marketplace__version-num {
  font-family: var(--font-mono);
}

/* G-MKT-02: Featured extensions landing page */
.marketplace__featured {
  display: flex;
  flex-direction: column;
  gap: 10px;
  padding: 4px 0;
}

.marketplace__featured-title {
  margin: 0;
  font-size: 0.9rem;
  font-weight: 600;
  color: var(--color-text-primary, #f0f0f0);
  letter-spacing: 0.01em;
}

/* G-MKT-02: README rendering area in detail view */
.marketplace__readme {
  margin-top: 8px;
  padding: 12px;
  border: 1px solid var(--color-border-default, #2a2a2c);
  border-radius: var(--radius-md);
  background: var(--color-bg-surface-container-low, #161616);
  font-size: 0.8125rem;
  line-height: 1.55;
  color: var(--color-text-secondary, #a0a0a0);
  overflow-x: auto;
}

.marketplace__readme :deep(h1),
.marketplace__readme :deep(h2),
.marketplace__readme :deep(h3),
.marketplace__readme :deep(h4) {
  margin: 12px 0 6px;
  color: var(--color-text-primary, #f0f0f0);
  font-weight: 600;
}

.marketplace__readme :deep(h1) { font-size: 1.1rem; }
.marketplace__readme :deep(h2) { font-size: 1rem; }
.marketplace__readme :deep(h3) { font-size: 0.9rem; }
.marketplace__readme :deep(h4) { font-size: 0.85rem; }

.marketplace__readme :deep(p) {
  margin: 6px 0;
}

.marketplace__readme :deep(a) {
  color: var(--color-primary, #4285f4);
  text-decoration: none;
}

.marketplace__readme :deep(a:hover) {
  text-decoration: underline;
}

.marketplace__readme :deep(code) {
  font-family: var(--font-mono);
  font-size: 0.78rem;
  padding: 1px 4px;
  border-radius: var(--radius-sm);
  background: color-mix(in srgb, var(--color-primary, #4285f4) 12%, transparent);
}

.marketplace__readme :deep(pre) {
  margin: 8px 0;
  padding: 8px;
  border-radius: var(--radius-sm);
  background: var(--color-bg-surface-container, #0d0d0d);
  overflow-x: auto;
}

.marketplace__readme :deep(pre code) {
  padding: 0;
  background: transparent;
}

.marketplace__readme :deep(ul),
.marketplace__readme :deep(ol) {
  margin: 6px 0;
  padding-left: 20px;
}

.marketplace__readme :deep(ul) { list-style: disc; }
.marketplace__readme :deep(ol) { list-style: decimal; }

.marketplace__readme :deep(li) {
  margin: 3px 0;
}

.marketplace__readme :deep(table) {
  border-collapse: collapse;
  width: 100%;
  margin: 8px 0;
}

.marketplace__readme :deep(th),
.marketplace__readme :deep(td) {
  border: 1px solid var(--color-border-default, #2a2a2c);
  padding: 6px 8px;
  text-align: left;
}

.marketplace__readme :deep(img) {
  max-width: 100%;
  height: auto;
}

.marketplace__readme :deep(blockquote) {
  margin: 8px 0;
  padding: 6px 12px;
  border-left: 3px solid var(--color-primary, #4285f4);
  background: color-mix(in srgb, var(--color-primary, #4285f4) 6%, transparent);
  color: var(--color-text-secondary, #a0a0a0);
}

.marketplace__readme :deep(hr) {
  border: none;
  border-top: 1px solid var(--color-border-default, #2a2a2c);
  margin: 12px 0;
}

.marketplace__readme-loading {
  display: flex;
  align-items: center;
  gap: 6px;
  padding: 12px;
  font-size: 0.8125rem;
  color: var(--color-text-tertiary, #707070);
}

/* G-MKT-02: Updates bar in installed list */
.marketplace__updates-bar {
  display: flex;
  align-items: center;
  gap: 8px;
  padding: 6px 4px 10px;
  flex-wrap: wrap;
}

/* G-MKT-02: Update tag on installed extension items */
.marketplace__update-tag {
  margin-left: auto;
}

@media (prefers-reduced-motion: reduce) {
  .marketplace__item,
  .marketplace__tab,
  .marketplace__back {
    transition: none;
  }
}
</style>
