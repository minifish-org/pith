# tools：内置工具、本地环境与可运行交付

当前执行顺序：tools-env → tools-read → tools-write → tools-edit → tools-bash → tools-delivery。源码/输出数量以 plan.json 为准。

上游内置执行工具是 read、write、edit、bash 四个；工具调用机制归 core。本模块也完成 Go 本地环境与新增 CLI，作为三个模块的集成和交付出口。

模块只在全部批次与最终验收通过后提交一次。下列批次是 Portsmith 内部执行顺序，不需要用户逐一输入命令。全部批次的执行材料已准备，尚未生成产品代码。

| 内部批次         | 范围                    | 直接前置                                        | 来源文件 |
| ---------------- | ----------------------- | ----------------------------------------------- | -------- |
| `tools-env`      | 本地执行环境与输出基础  | 前置模块已接受                                  | 8        |
| `tools-read`     | 完整 read               | tools-env                                       | 1        |
| `tools-write`    | 完整 write              | tools-env                                       | 1        |
| `tools-edit`     | 完整 edit 与 diff       | tools-write                                     | 2        |
| `tools-bash`     | 完整 bash               | tools-env                                       | 1        |
| `tools-delivery` | 完整入口与可运行 Go CLI | tools-read, tools-write, tools-edit, tools-bash | 3        |

## tools-env / 本地执行环境与输出基础

行为：

- NodeExecutionEnv -> LocalExecutionEnv；fs/shell 语义
- 路径解析、截断、输出捕获/落盘、publisher
- 同文件写入队列、图片 sniff/base64

验收：

- temp dir 文件/链接/权限/编码、输出上限、进程取消/超时/后代回收；macOS/Linux 原生执行验证

完整来源与输出见 plan.json 对应批次。参考输入已由 reference-ledger.json 分派并放入对应步骤只读 testdata；来源与 judge 按行读取。

## tools-read / 完整 read

行为：

- 文本 offset/limit/truncation、~/@ 路径处理
- 图片/BMP 与 ReadImageProcessor 注入、错误详情

验收：

- 逐 tools/truncate TS 场景；UTF-8/CRLF/超长首行；图像 bytes/MIME、无效图片、取消

完整来源与输出见 plan.json 对应批次。参考输入已由 reference-ledger.json 分派并放入对应步骤只读 testdata；来源与 judge 按行读取。

## tools-write / 完整 write

行为：

- 自动建目录、写入结果、取消、序列化文件操作

验收：

- 空文件/覆盖/多字节/缺目录/权限失败；并发 edit/write 同文件顺序；失败无假成功

完整来源与输出见 plan.json 对应批次。参考输入已由 reference-ledger.json 分派并放入对应步骤只读 testdata；来源与 judge 按行读取。

## tools-edit / 完整 edit 与 diff

行为：

- 精确/模糊匹配、重复匹配、BOM/换行保持
- diff 和 changed line 信息、file mutation queue

验收：

- 唯一匹配/多匹配/不存在/Unicode/BOM/CRLF；并发重读；diff fixtures

完整来源与输出见 plan.json 对应批次。参考输入已由 reference-ledger.json 分派并放入对应步骤只读 testdata；来源与 judge 按行读取。

## tools-bash / 完整 bash

行为：

- prepare/execution、cwd/env、timeout/abort、exit code
- 输出流、head/tail/truncation、临时输出文件与更新

验收：

- 非零退出、信号、超量/二进制输出、timeout/abort、子进程清理、资源关闭；不以 shell 当文件权限沙箱

完整来源与输出见 plan.json 对应批次。参考输入已由 reference-ledger.json 分派并放入对应步骤只读 testdata；来源与 judge 按行读取。

## tools-delivery / 完整入口与可运行 Go CLI

行为：

- 工具注册、Go SDK facade、LocalExecutionEnv 导出
- 新增 cmd/pith：provider/model、凭据、root/cwd、prompt、events、session
- macOS/Linux 构建产物与示例，运行不依赖 Node/Go/npm

验收：

- 假服务完整 read/write/edit/bash 多轮与会话恢复；缺配置非零退出；纯净目录运行 binary；真实 provider smoke 独立记录

完整来源与输出见 plan.json 对应批次。参考输入已由 reference-ledger.json 分派并放入对应步骤只读 testdata；来源与 judge 按行读取。

## 独立交付

支持用户配置任意已实现 provider/model，不能把生成代码所用 DeepSeek 固化为产品唯一模型。最终 CLI 可构建到一个 Go binary；catalog 等数据 embed，凭据外置；bash 使用系统 shell。

CLI 是新增 Go 集成，不伪称对应某个 TS e2e 测试。工具权限策略可注入；root/cwd 默认是工作目录，不自动等于安全沙箱。
