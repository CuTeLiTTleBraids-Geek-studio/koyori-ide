// P19 CI 修复：@wailsio/runtime 的 drag.js 在模块加载时即启动
// window.setInterval 轮询（≤100 次 × 50ms，等待 wails 环境就绪）。任何未
// mock 该运行时的测试文件只要（经 api 包装层 → 生成 bindings）加载了它，
// 轮询定时器就可能在 vitest 环境销毁后触发 "ReferenceError: window is
// not defined"，被 vitest 记为 Unhandled Error 并使整个测试进程退出 1
// （2026-08-29 在 CI 的 ubuntu/macos 腿先后复现，且是否触发取决于调度
// 时序，属平台相关 flake）。
//
// 本模块作为 setupFiles 注册的默认桩：通过 vi.mock 在加载前替换
// "@wailsio/runtime"，从源头阻断 drag.js 的加载。测试文件内自己声明的
// vi.mock("@wailsio/runtime", ...) 优先级更高，会覆盖此默认桩，因此既有
// 的 10+ 处局部 mock 语义不受影响。
//
// 生产代码只使用 Events（On/Emit 等）；生成 bindings 另外按名导入
// Call / CancellablePromise / Create。桩的 Events 实现了一个纯本地
// 发布/订阅总线，保证依赖 emit → on 往返的测试行为不依赖真实 IPC。

type WailsEventHandler = (data?: unknown) => void;

const listeners = new Map<string, Set<WailsEventHandler>>();

function addListener(name: string, handler: WailsEventHandler): () => void {
  let set = listeners.get(name);
  if (!set) {
    set = new Set();
    listeners.set(name, set);
  }
  set.add(handler);
  return () => {
    set?.delete(handler);
  };
}

function dispatch(name: string, data?: unknown): void {
  for (const handler of listeners.get(name) ?? []) {
    try {
      handler(data);
    } catch {
      // 与真实运行时一致：单个订阅者异常不中断其余分发。
    }
  }
}

export const Events = {
  On: (name: string, handler: WailsEventHandler) => addListener(name, handler),
  OnMultiple:
    (name: string, handler: WailsEventHandler, maxCallbacks: number) => {
      let remaining = maxCallbacks;
      const cancel = addListener(name, (data) => {
        handler(data);
        if (--remaining <= 0) cancel();
      });
      return cancel;
    },
  Off: (name: string, handler: WailsEventHandler) => {
    listeners.get(name)?.delete(handler);
  },
  Emit: async (name: string, data?: unknown) => {
    dispatch(name, data);
  },
};

function notStubbed(..._args: unknown[]): never {
  throw new Error("@wailsio/runtime is stubbed in vitest: backend calls are unavailable");
}

// 生成 bindings 的调用面：Call.ByName/ByID 在测试环境没有后端，统一
// fail-closed 抛错；Create.* 仅在参数封送时被调用，透传原值即可。
export const Call = {
  ByName: notStubbed,
  ByID: notStubbed,
};

export const Create = {
  Any: (value: unknown) => value,
  Array: () => (value: unknown) => value,
  Map: () => (value: unknown) => value,
  Nullable: () => (value: unknown) => value,
  Struct: () => (value: unknown) => value,
};

export class CancellablePromise<T> {
  constructor(executor: (resolve: (v: T) => void, reject: (e: unknown) => void) => void) {
    executor(
      () => undefined,
      () => undefined,
    );
  }
  then(): never {
    throw new Error("@wailsio/runtime is stubbed in vitest");
  }
  catch(): never {
    throw new Error("@wailsio/runtime is stubbed in vitest");
  }
}
