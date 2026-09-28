# Pith

将固定版本 Pi 的 **AI 层、稳定 Agent 内核与内置工具**迁移为可嵌入、可独立交付的 Go SDK 和 CLI。迁移工具是独立的 [Portsmith](https://github.com/minifish-org/portsmith)，规划由 Codex 等外部 AI 或人工完成。

**当前状态：完整执行材料已准备，可以启动 `ai → core → tools` 全流程。** 3 个模块、24 个批次、26 个自动步骤。正式 Go 代码由你运行后生成。直接看 [启动命令和使用说明](migration/start.md)。

先看 [迁移计划](migration/README.md)：只面对 `ai → core → tools` 三个模块；模块内分批，完成模块后提交。保持上游目录职责，逐文件/符号记录来源，便于持续移植。

- [目录与文件对应](migration/inventory.json)：来源版本/hash、目标路径、所属模块和批次。
- [完整验收](migration/validation.md)：所有 provider、认证、稳定 harness、read/write/edit/bash。
- [执行准备](migration/execution.md)：执行材料与自检证据。
- [持续更新](migration/continuous-port.md)：新上游版本的差异、增量计划与验证。

旧 9 项最小计划和 48 项测试已归档在 [legacy/mvp-1](migration/legacy/mvp-1/ARCHIVED.md)，不代表完整迁移已准备可执行。

## 现在检查计划

```sh
node ../portsmith/node_modules/tsx/dist/cli.mjs migration/check-plan.mts
```

以上仅检查计划，不调用模型。正式启动见启动说明；正式命令才会使用真实模型生成候选，完整模块通过后提交。

## 迁移完成后的交付

Go 应用可导入 SDK；个人用户可直接运行编译好的 pith，配置 provider/model/凭据，完成读取、写入、编辑文件和执行命令。程序不需要 Node/npm 或 Go 工具链；bash 工具需要系统 shell。CLI 参数已在 tools-delivery 契约固定。

本次不包括 TUI/Web/桌面 App、Computer Use、MCP 或 experimental/pico3。保持原有 [AGPL 许可证](LICENSE)；来自 Pi 的 MIT 归属见 [UPSTREAM-LICENSE](migration/UPSTREAM-LICENSE)。
