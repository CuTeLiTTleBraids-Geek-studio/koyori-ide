import { describe, expect, it } from "vitest";
import { expandAbbreviation } from "emmet-monaco-es";

describe("emmet-monaco-es engine", () => {
  it("expands HTML and Vue-template markup abbreviations", () => {
    const expanded = expandAbbreviation("ul>li.item*2", {
      type: "markup",
      syntax: "html",
    });

    expect(expanded).toContain("<ul>");
    expect(expanded.match(/<li class="item">/g)).toHaveLength(2);
  });

  it("expands CSS-family and JSX abbreviations with the intended syntax", () => {
    expect(expandAbbreviation("m10", {
      type: "stylesheet",
      syntax: "css",
    })).toContain("margin: 10px;");

    expect(expandAbbreviation("div.card", {
      type: "markup",
      syntax: "jsx",
    })).toContain('className="card"');
  });
});
