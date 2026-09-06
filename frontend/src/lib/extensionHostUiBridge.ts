import { h, nextTick, ref } from "vue";
import { ElMessageBox } from "element-plus";
import type {
  InputBoxOptions,
  QuickPickItem,
  QuickPickOptions,
} from "@/lib/extensionHost/vscodeApi";

function isCancelled(error: unknown): boolean {
  return error === "cancel" || error === "close";
}

function validationMessage(value: unknown): string | undefined {
  return typeof value === "string" && value.length > 0 ? value : undefined;
}

function showValidationError(
  instance: { inputErrorMessage: string; editorErrorMessage: string; validateError: boolean },
  message: string,
): void {
  // Element Plus validates again from an input watcher after the native input
  // event. Wait for that tick before publishing the async error so it cannot
  // clear the visible message.
  void nextTick().then(() => {
    instance.inputErrorMessage = message;
    instance.editorErrorMessage = message;
    instance.validateError = true;
  });
}

/** Bridge vscode.window.showInputBox to the real Element Plus dialog. */
export async function showExtensionInputBox(
  options?: InputBoxOptions,
): Promise<string | undefined> {
  try {
    const result = await ElMessageBox.prompt(options?.prompt ?? "", "Extension input", {
      inputValue: options?.value ?? "",
      inputPlaceholder: options?.placeHolder,
      inputType: options?.password ? "password" : "text",
      // Validate only in beforeClose. Element Plus' synchronous validator is
      // also called by an input watcher, which would erase a later async error.
      beforeClose: (action, instance, done) => {
        if (action !== "confirm" || !options?.validateInput) {
          done();
          return;
        }
        const inputs = document.querySelectorAll<HTMLInputElement>(".el-message-box__input input");
        const input = inputs.item(inputs.length - 1)?.value ?? options.value ?? "";
        let validation: string | undefined | null | PromiseLike<string | undefined | null>;
        try {
          validation = options.validateInput(input);
        } catch (error) {
          const message = error instanceof Error ? error.message : String(error);
          showValidationError(instance, message);
          return;
        }
        const issue = validationMessage(validation);
        if (!validation || typeof (validation as PromiseLike<unknown>).then !== "function") {
          if (issue) {
            showValidationError(instance, issue);
            return;
          }
          done();
          return;
        }
        instance.confirmButtonLoading = true;
        instance.editorErrorMessage = "";
        instance.validateError = false;
        void Promise.resolve(validation)
          .then((asyncIssue) => {
            instance.confirmButtonLoading = false;
            const message = validationMessage(asyncIssue);
            if (message) {
              showValidationError(instance, message);
              return;
            }
            done();
          })
          .catch((error: unknown) => {
            instance.confirmButtonLoading = false;
            const message = error instanceof Error ? error.message : String(error);
            showValidationError(instance, message);
          });
      },
      confirmButtonText: "OK",
      cancelButtonText: "Cancel",
      closeOnClickModal: options?.ignoreFocusOut !== true,
    });
    return result.value;
  } catch (error) {
    if (isCancelled(error)) return undefined;
    throw error;
  }
}

/** Bridge vscode.window.showQuickPick to a real, cancellable list dialog. */
export async function showExtensionQuickPick(
  items: string[] | QuickPickItem[],
  options?: QuickPickOptions,
): Promise<string | QuickPickItem | (string | QuickPickItem)[] | undefined> {
  const labels = items.map((item) => typeof item === "string" ? item : item.label);
  const picked = items
    .map((item, index) => typeof item !== "string" && item.picked ? String(index) : "")
    .filter(Boolean);
  const selected = ref<string | string[]>(options?.canPickMany ? picked : (picked[0] ?? ""));
  const hasSingleSelection = options?.canPickMany !== true && picked.length > 0;
  const placeholder = options?.placeHolder ?? "Select an item";
  const optionNodes = [
    ...(options?.canPickMany !== true && !hasSingleSelection
      ? [h("option", { value: "", disabled: true, selected: true }, placeholder)]
      : []),
    ...labels.map((label, index) => h("option", {
      value: String(index),
      selected: picked.includes(String(index)),
    }, label)),
  ];
  const select = h("select", {
    multiple: options?.canPickMany === true,
    autofocus: true,
    "aria-label": placeholder,
    style: "width:100%;min-height:36px;",
    onChange: (event: Event) => {
      const target = event.target as HTMLSelectElement;
      selected.value = target.multiple
        ? Array.from(target.selectedOptions, (option) => option.value).filter(Boolean)
        : target.value;
    },
  }, optionNodes);

  try {
    await ElMessageBox({
      title: "Extension quick pick",
      message: select,
      showCancelButton: true,
      confirmButtonText: "Select",
      cancelButtonText: "Cancel",
      closeOnClickModal: options?.ignoreFocusOut !== true,
    });
    const values = Array.isArray(selected.value) ? selected.value : [selected.value];
    const chosen = values
      .filter((value) => value !== "")
      .map((value) => items[Number(value)])
      .filter((item): item is string | QuickPickItem => item !== undefined);
    return options?.canPickMany ? chosen : chosen[0];
  } catch (error) {
    if (isCancelled(error)) return undefined;
    throw error;
  }
}
