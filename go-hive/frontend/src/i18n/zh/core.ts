/**
 * Core shell strings (sidebar, header, shared chrome).
 *
 * Keys are the English source text — exactly what the UI renders in `en`.
 * This file is owned by the i18n-core lane; page/chat/settings strings live
 * in the sibling dictionaries.
 */
export const coreZh: Record<string, string> = {
  // Sidebar — navigation
  "New Chat": "新建对话",
  "Org Chart": "组织架构",
  "Configuration": "配置",
  "Models": "模型",
  "Credentials": "凭据",
  "Skills": "技能",
  "Memory": "记忆",
  "Colonies": "蜂群",
  "Queen Bees": "蜂后",
  "Create colony": "创建蜂群",
  "No colonies yet": "暂无蜂群",
  "Loading queens...": "正在加载蜂后…",
  "Loading colonies...": "正在加载蜂群…",
  "Expand sidebar": "展开侧边栏",
  "Collapse sidebar": "收起侧边栏",
  "Language": "语言",

  // Sidebar — update banner
  "Update available — download OpenHive v{version}": "有可用更新 — 下载 OpenHive v{version}",
  "v{version} is now available": "v{version} 现已可用",

  // Create colony dialog
  "Create Colony": "创建蜂群",
  "Queen Bee": "蜂后",
  "Colony Name": "蜂群名称",
  "Goal": "目标",
  "(optional)": "（可选）",
  "e.g. research team": "例如 research_team",
  "Describe what this colony should work on": "描述这个蜂群要处理的工作",
  "Cancel": "取消",
  "Create": "创建",
  "Creating...": "创建中…",

  // Colony row menu + delete dialog
  "Rename": "重命名",
  "Delete colony": "删除蜂群",
  "Delete": "删除",
  "Failed to rename": "重命名失败",
  "Running on cloud computer": "在云端计算机上运行",
  "Cloud": "云端",
  "Are you sure you want to delete": "确定要删除",
  "This agent and all its tracked data will be permanently deleted.":
    "该智能体及其所有跟踪数据将被永久删除。",
  "This agent will be removed from your colonies.": "该智能体将从你的蜂群中移除。",
  "This colony also runs on your cloud computer — it will be removed there too.":
    "该蜂群也在你的云端计算机上运行 — 那里也会将其移除。",
  "I want to completely remove the data tracked in this colony":
    "我要彻底删除该蜂群跟踪的数据",

  // Queen / colony live status
  "Interrupted": "已中断",
  "Interrupted: {cause}": "已中断：{cause}",
  "interrupted": "已中断",
  "interrupted ({cause})": "已中断（{cause}）",
  "Running: {tool}": "正在运行：{tool}",
  "running {tool}": "正在运行 {tool}",
  "Thinking…": "思考中…",
  "Working": "工作中",
  "working": "工作中",

  // Header
  "Account": "账户",
  "Profile & settings": "个人资料与设置",
  "Tutorial": "教程",
  "New": "新",
  "View {name}'s profile": "查看 {name} 的主页",
  "View colony settings": "查看蜂群设置",
};
