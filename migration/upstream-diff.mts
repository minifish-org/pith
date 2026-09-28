import assert from "node:assert/strict";
import { lstat } from "node:fs/promises";
import path from "node:path";
import {
  diffInputs,
  loadBundle,
  scanInputs,
  validateStructure,
} from "./plan-lib.mts";

const args = process.argv.slice(2);
assert(
  args.length === 2 && args[0] === "--source",
  "用法：upstream-diff.mts --source /absolute/path/to/new/pi",
);
const root = path.resolve(args[1]);
assert((await lstat(root)).isDirectory(), "source 必须是已取得的 Pi 源码目录");
const bundle = await loadBundle();
validateStructure(bundle);
const inputs = await scanInputs(root, bundle.inventory);
assert(
  inputs.has("packages/ai/src/types.ts") &&
    inputs.has("packages/agent/src/agent.ts"),
  "不是可识别的 Pi 源码；如上游重构入口，先人工规划目录变化",
);
console.log(JSON.stringify(diffInputs(bundle, inputs), null, 2));
