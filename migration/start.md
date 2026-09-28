# 开始完整迁移

执行材料已准备：**3 个模块、24 个批次、26 个自动步骤**。现在可以正式启动；不用先手动完成 EventStream，也不用逐个填写 unit、judge、out 或 task。

在终端复制：

```sh
cd ~/work/pith
node ../portsmith/dist/cli.js migrate --plan migration --commit --env-file ../omni-pi/.env --max-turns 80 --timeout 1800
```

它会读取你已有的 DeepSeek 配置，然后自动按 `ai → core → tools` 生成、验证、修复、继续。每个完整模块验收通过后提交一次，不自动 push。首次运行还会把允许的准备材料提交为一条 chore 记录。

- `--plan migration`：使用这个完整计划。
- `--commit`：允许自动保存准备材料，以及验收通过的三个模块提交。
- `--env-file ../omni-pi/.env`：读取现有配置，不需要复制密钥。
- `--max-turns 80 --timeout 1800`：每次生成/修复最多 80 轮、30 分钟；不是整个迁移的时间上限。

想先检查就绪状态，可以运行：

```sh
node ../portsmith/dist/cli.js migrate --plan migration --check
```

应输出 `ready`、`canStart: true`、`readyBatches: 24`、`preparedSteps: 26`、`blocked: []`。检查不调用模型、不创建候选、不提交。

## 去哪里看

- 终端显示当前模块、步骤和生成/修复进度。
- `.portsmith/runs/`：未完成的候选、模型记录及 `verification.json`。
- `.portsmith/modules.json`：自动步骤与模块进度。
- `packages/`、`cmd/pith/`：模块完整验收后才写入的正式 Go 代码。
- `migration/results/`：每个已接受模块的验证记录。

按 Ctrl+C 可以停止。网络、模型、编译或验收失败会保留候选；处理报错后重跑同一个命令继续。不要删除 `.portsmith` 来重试。正常情况下不会再遇到本计划的 `planned/partial` 材料占位；有限次修复仍可能无法自动解决代码问题。

## 已验证的范围

来源锁定、所有任务快照、独立 judge 编译/错误对照、依赖兼容探针和执行器离线连续执行均已检查。226 条固定 TS 行为对照，加上 EventStream 专用测试与最终 CLI 验收，用于检查未来候选。

**准备完成不表示 Go 迁移已经完成。** 本次没有调用 DeepSeek、没有生成正式 Pith Go 实现。完整协议等价、平台运行和实际模型集成以生成后的验证结果为准。各个 SDK 函数的 Go 签名在生成过程中按固定规则选定并编译，不要求你再来手写一遍。

详细操作见 [Portsmith 使用说明书](../../portsmith/docs/user-manual.md)。验收器自检见 [audit.json](readiness/audit.json)，依赖探针见 [dependencies.json](readiness/dependencies.json)。
