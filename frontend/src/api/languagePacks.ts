import * as LanguagePackServiceBindings from "../../bindings/github.com/CuTeLiTTleBraids-Geek-studio/koyori-ide/services/languagepackservice.js";
import type { LanguagePackInfo } from "../../bindings/github.com/CuTeLiTTleBraids-Geek-studio/koyori-ide/services/models.js";
import type { LanguagePackRuntimeContribution } from "@/lib/languagePackRuntime";
import { replaceExternalLanguagePackContributions } from "@/lib/language";

function unwrap<T>(value: T | null | undefined, fallback: T): T {
  return value == null ? fallback : value;
}

export const languagePackService = {
  list: async (): Promise<LanguagePackInfo[]> =>
    unwrap(await LanguagePackServiceBindings.ListLanguagePacks(), []),
  install: (): Promise<LanguagePackInfo> =>
    LanguagePackServiceBindings.InstallLanguagePack(),
  disable: (id: string): Promise<void> =>
    LanguagePackServiceBindings.DisableLanguagePack(id),
  enable: (id: string): Promise<void> =>
    LanguagePackServiceBindings.EnableLanguagePack(id),
  rollback: (id: string): Promise<LanguagePackInfo> =>
    LanguagePackServiceBindings.RollbackLanguagePack(id),
  uninstall: (id: string): Promise<void> =>
    LanguagePackServiceBindings.UninstallLanguagePack(id),
  lastError: (): Promise<string> => LanguagePackServiceBindings.GetLastError(),
  refreshRuntime: async (): Promise<void> => {
    try {
      const contributions = unwrap(
        await LanguagePackServiceBindings.ListActiveExternalLanguagePackContributions(),
        [],
      ) as LanguagePackRuntimeContribution[];
      replaceExternalLanguagePackContributions(contributions);
    } catch (error) {
      replaceExternalLanguagePackContributions([]);
      throw error;
    }
  },
};

export type { LanguagePackInfo };
