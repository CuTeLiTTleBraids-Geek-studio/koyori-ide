// Koyori IDE 模块 · Notifications。
// 喵，这是 Koyori IDE 的 Notifications 模块（前端实现）~
import { ElNotification } from "element-plus";

type NotificationType = "success" | "warning" | "info" | "error";

interface NotifyOptions {
  title?: string;
  message: string;
  type?: NotificationType;
  duration?: number;
}

export function notify(options: NotifyOptions): void {
  const type = options.type ?? "info";
  const duration = options.duration ?? 3000;
  ElNotification({
    title: options.title,
    message: options.message,
    type,
    duration,
    position: "bottom-right",
  });
}

export function notifySuccess(message: string, title?: string): void {
  notify({ message, title, type: "success" });
}

export function notifyError(message: string, title?: string): void {
  notify({ message, title, type: "error", duration: 5000 });
}

export function notifyWarning(message: string, title?: string): void {
  notify({ message, title, type: "warning" });
}

export function notifyInfo(message: string, title?: string): void {
  notify({ message, title, type: "info" });
}
