import { describe, expect, it } from "vitest";
import en from "@/lib/locales/en";
import zh from "@/lib/locales/zh";
import ja from "@/lib/locales/ja";

describe("plan honesty copy (P14-G35)", () => {
  it("says empty plans are valid and never invented", () => {
    for (const [name, dict] of [["en", en], ["zh", zh], ["ja", ja]] as const) {
      const hint = dict["planSection.hint"];
      const noSteps = dict["planPanel.noSteps"];
      expect(hint, name + " hint").toMatch(/empty|Empty|空|合法/);
      expect(noSteps, name + " noSteps").toMatch(/empty|Empty|空|合法/);
      expect(hint, name + " hint wired").not.toMatch(/not wired|未接线|未配線/i);
      expect(noSteps, name + " noSteps wired").not.toMatch(/not wired|未接线|未配線/i);
    }
  });
});
