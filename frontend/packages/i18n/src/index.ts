const messages = { loading: "正在加载", retry: "重试", empty: "暂无数据" } as const;
export const t = (key: keyof typeof messages) => messages[key];
