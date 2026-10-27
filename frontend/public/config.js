// 运行时配置（本地开发默认值，Docker 环境会被 entrypoint 脚本覆盖）
window.__RUNTIME_CONFIG__ = {
  MAX_FILE_SIZE_MB: 200,
  MAX_SKILL_BUNDLE_SIZE_MB: 256,
  // LLM 增强总开关（摘要/问题生成/图谱/自动标签/Wiki/多模态），默认关闭。
  // 与后端 WEKNORA_ENRICHMENT_ENABLED 同源，Docker 环境由 entrypoint 注入。
  ENRICHMENT_ENABLED: false,
  // Optional: serve embed on a dedicated origin, e.g. 'https://embed.example.com'
  EMBED_BASE_URL: '',
  // Optional: default UI locale for first-time visitors (zh-CN | zh-TW | en-US | ru-RU | ko-KR | ja-JP)
  DEFAULT_LOCALE: '',
};
