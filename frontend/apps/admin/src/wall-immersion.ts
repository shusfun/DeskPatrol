export const WALL_TOOLBAR_HIDE_DELAY_MS = 2500;

type TimerScheduler = {
  setTimeout: (callback: () => void, delay: number) => number;
  clearTimeout: (timer: number) => void;
};

export function createToolbarAutoHideController(onVisibleChange: (visible: boolean) => void, scheduler: TimerScheduler = window) {
  let hideTimer: number | null = null;
  const cancelHide = () => {
    if (hideTimer !== null) scheduler.clearTimeout(hideTimer);
    hideTimer = null;
  };
  return {
    cancelHide,
    scheduleHide() {
      cancelHide();
      hideTimer = scheduler.setTimeout(() => {
        hideTimer = null;
        onVisibleChange(false);
      }, WALL_TOOLBAR_HIDE_DELAY_MS);
    },
    show() {
      cancelHide();
      onVisibleChange(true);
    },
  };
}

export function isImmersionExitKey(key: string) {
  return key === "Escape";
}

export function moveDeviceOrder(order: string[], source: string, target: string) {
  const from = order.indexOf(source);
  const to = order.indexOf(target);
  if (from < 0 || to < 0 || from === to) return order;
  const next = [...order];
  next.splice(to, 0, next.splice(from, 1)[0]);
  return next;
}
