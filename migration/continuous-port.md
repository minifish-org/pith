# 持续移植：保留对应关系，按上游差异更新

首次迁移固定 upstream.json 的 commit。`inventory.json` 记录源文件 hash/目标路径/批次/符号；模块接受后结果记录增加 Go commit、目标文件 hash、contract/judge hash 和差异 ID。**当前还没有 accepted Go baseline**，不能把 planned 映射当成已移植基线。

## 更新流程

1. 保留旧 Pi 源码及已接受 Go commit，另取新版本源码到独立目录。下载/校验必须明确版本与许可证；不要覆盖旧缓存。
2. 对源文件、测试、fixture、脚本、package/config、生成数据做差异清点。新增/删除/改名都要报告；改名只提示候选，不能自动继承语义。
3. 根据映射找到直接受影响目标，再追踪旧 import 图反向依赖、共享契约、各模块依赖。ai 更新需要 core/tools 回归；core 更新需要 tools 回归。
4. Codex 等外部规划者审查新行为/新依赖，补映射、契约、测试和增量 plan。新文件默认 unclassified，不自动套旧目录规则后当完成；范围外变化单独报告。
5. Portsmith 按增量计划执行；保持已接受 Go 文件和客户定制，冲突进入人工/规划修复，不覆盖为全新生成文件。
6. 所有受影响检查通过后，用一次对应模块更新提交纳入 Go 变更、来源映射与新的接受基线。未完成模块仍明确指向旧版本，不能全局提前提升 revision。

## 当前可用的只读差异工具

取得新源码后，在 Pith 根目录：

```sh
node ../portsmith/node_modules/tsx/dist/cli.mjs migration/upstream-diff.mts --source /absolute/path/to/new/pi
```

该脚本不下载、不调用模型、不编辑计划、不 port、不提交。它以当前 inventory 为比较基线，输出 JSON：changed/added/deleted、可能的 identical-content rename、受影响模块、需回归模块、未分类文件及 generated catalog 变化。报告只用于规划；不是新源码的信任/版本证明，也不等于增量计划已完成。

比较当前锁定源码应为零变更；修改范围内文件应触发相应模块。script/config/test 变更也必须被报告。新 root package 或改变模块依赖的情况仍需重新分析完整仓库，不能只看字符串路径。

## 需要持久化的记录

模块 accepted record：upstream repo+commit+tag、source hashes、plan/contract/judge/dependency hashes、Go base/accepted commit、target hashes、生成数据 artifact hashes、逐符号覆盖、测试/platform/live-smoke 状态、允许差异、下游定制记录。

更新时先对比 target hashes 判断用户改动。采用 base→upstream-new 与 base→downstream 的三方比较；无法自动合并时留下差异报告。删除源 API 必须检查 Go consumer 和迁移说明；一个来源拆/合文件保留多对多映射，不丢掉历史来源。

目前未实现自动周更、自动规划、自动 push。先按此流程手动规划与验证每个上游版本。
