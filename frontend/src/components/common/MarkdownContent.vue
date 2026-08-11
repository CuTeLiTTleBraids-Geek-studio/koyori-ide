<script lang="ts">
// Koyori IDE 组件 · Markdown Content。
// 喵，这是 Markdown Content，负责 Koyori IDE 的界面呈现喵~
// MED-03: MarkdownContent is the single safe boundary for rendering
// pre-sanitized HTML. It uses a render function (h) with innerHTML instead
// of the v-html directive, so the eslint vue/no-v-html rule stays effective
// across the rest of the app.
//
// Callers MUST pass HTML that has already been run through DOMPurify
// (renderMarkdown / renderMarkdownWithApplyButtons). Fallthrough attrs
// (class, style, @click) are merged onto the rendered div so callers can
// style and interact with the content exactly as before.
import { h, type FunctionalComponent } from "vue";

interface MarkdownContentProps {
  html: string;
}

const MarkdownContent: FunctionalComponent<MarkdownContentProps> = (
  props,
  { attrs, slots },
) => {
  // When no html is provided, fall back to the default slot so callers can
  // use this component as a plain styled container too.
  if (!props.html) {
    return h("div", attrs, slots.default?.());
  }
  // Defensive: in dev mode, warn if the HTML looks unsanitized.
  if (import.meta.env?.DEV) {
    if (/<script[\s>]/i.test(props.html) || /\son\w+\s*=/i.test(props.html)) {
      console.warn(
        "[MarkdownContent] HTML appears to contain unsanitized content (<script> or event handler). " +
        "Ensure all HTML is passed through DOMPurify before rendering.",
      );
    }
  }
  return h("div", { ...attrs, innerHTML: props.html });
};

MarkdownContent.props = ["html"];
MarkdownContent.inheritAttrs = true;

export default MarkdownContent;
</script>
