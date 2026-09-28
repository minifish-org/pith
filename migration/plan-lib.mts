import { createHash } from "node:crypto";
import { lstat, readFile, readdir } from "node:fs/promises";
import path from "node:path";
import { fileURLToPath } from "node:url";

export const migrationRoot = fileURLToPath(new URL("./", import.meta.url));
export const projectRoot = path.dirname(migrationRoot.replace(/\/$/, ""));
export const sha256 = (data: string | Buffer) =>
  createHash("sha256").update(data).digest("hex");
export async function json(name: string) {
  return JSON.parse(await readFile(path.join(migrationRoot, name), "utf8"));
}
export function ensure(condition: unknown, message: string): asserts condition {
  if (!condition) throw Error(message);
}
export function safeRelative(name: string) {
  ensure(
    typeof name === "string" &&
      !!name &&
      !path.isAbsolute(name) &&
      !name.includes("\\") &&
      !name.split("/").some((v) => !v || v === "." || v === ".."),
    `不安全路径：${name}`,
  );
}
export function dag(
  items: { id: string; dependsOn: string[] }[],
  label: string,
) {
  const nodes = new Map(items.map((i) => [i.id, i]));
  ensure(nodes.size === items.length, `${label} 重复 ID`);
  const done = new Set<string>(),
    visiting = new Set<string>();
  function visit(id: string) {
    ensure(nodes.has(id), `${label} 未知依赖：${id}`);
    if (done.has(id)) return;
    ensure(!visiting.has(id), `${label} 依赖成环：${id}`);
    visiting.add(id);
    for (const dep of nodes.get(id)!.dependsOn) visit(dep);
    visiting.delete(id);
    done.add(id);
  }
  for (const id of nodes.keys()) visit(id);
}
export async function loadBundle() {
  const [plan, inventory, exports, workflow, pin] = await Promise.all(
    [
      "plan.json",
      "inventory.json",
      "exports.json",
      "workflow.json",
      "upstream.json",
    ].map(json),
  );
  const analysisBytes = await readFile(
    path.join(migrationRoot, "analysis.json"),
  );
  return {
    plan,
    inventory,
    exports,
    workflow,
    pin,
    analysisBytes,
    analysis: JSON.parse(analysisBytes.toString()),
  };
}
export function baselineEntries(inventory: any) {
  return [
    ...inventory.files,
    ...inventory.referenceInputs,
    ...inventory.referenceAssets,
  ];
}
export function validateStructure(
  bundle: Awaited<ReturnType<typeof loadBundle>>,
) {
  const {
    plan: p,
    inventory: inv,
    exports: exp,
    workflow: w,
    pin,
    analysisBytes,
    analysis,
  } = bundle;
  ensure(
    p.version === 2 && inv.version === 2 && w.version === 2,
    "必须是模块计划 v2",
  );
  ensure(
    p.revision === pin.commit &&
      inv.revision === pin.commit &&
      exp.revision === pin.commit &&
      p.source === pin.sourceDir,
    "来源版本不一致",
  );
  ensure(p.analysisSha256 === sha256(analysisBytes), "分析快照 hash 不一致");
  ensure(
    p.modules.map((m: any) => m.id).join(",") === "ai,core,tools",
    "必须为 ai/core/tools 三模块",
  );
  ensure(
    w.moduleOrder.join(",") === "ai,core,tools" &&
      w.commit === "module" &&
      w.push === false,
    "执行顺序或提交策略不一致",
  );
  ensure(
    p.execution.ready === w.executionReady &&
      p.execution.ready === (p.execution.blockers.every((b: any) => b.status === "resolved") &&
        p.batches.every((b: any) => w.batches[b.id]?.status === "ready" && w.batches[b.id].steps.length > 0)),
    "执行准备状态与批次/阻塞项不一致",
  );
  ensure(
    w.blockers.join(",") ===
      p.execution.blockers.filter((b: any) => b.status !== "resolved").map((b: any) => b.id).join(","),
    "blocker 不一致",
  );
  dag(p.modules, "模块");
  dag(p.batches, "批次");
  const byBatch = new Map<string, any>(p.batches.map((b: any) => [b.id, b]));
  const owned = new Map<string, string>();
  const listed = p.modules.flatMap((m: any) => m.batches);
  ensure(
    listed.length === byBatch.size &&
      new Set(listed).size === byBatch.size &&
      listed.every((id: string) => byBatch.has(id)),
    "模块批次覆盖不完整",
  );
  for (const m of p.modules)
    for (const id of m.batches) {
      const b = byBatch.get(id)!;
      ensure(
        b.module === m.id &&
          b.behaviors.length &&
          b.acceptance.length &&
          b.sources.length,
        `批次范围缺失：${id}`,
      );
      for (const dep of b.dependsOn)
        ensure(
          byBatch.get(dep)?.module === m.id,
          `跨模块依赖须由模块边声明：${id}`,
        );
      for (const out of b.outputs) {
        safeRelative(out);
        ensure(!owned.has(out), `目标路径重复：${out}`);
        owned.set(out, id);
      }
    }
  const files = new Map<string, any>();
  const analysisFiles = new Map<string, any>(
    analysis.files.map((f: any) => [f.path, f]),
  );
  const exportFiles = new Map<string, any>(
    exp.files.map((f: any) => [f.source, f]),
  );
  ensure(
    exportFiles.size === inv.files.length &&
      exp.files.length === inv.files.length,
    "导出清单覆盖不一致",
  );
  for (const f of inv.files) {
    safeRelative(f.source);
    ensure(!files.has(f.source), `重复来源：${f.source}`);
    files.set(f.source, f);
    const old = analysisFiles.get(f.source);
    ensure(old && old.sha256 === f.sha256, `来源与扫描不一致：${f.source}`);
    ensure(
      f.reason &&
        ["port", "adapt", "excluded", "reference-only"].includes(f.disposition),
      `没有处置理由：${f.source}`,
    );
    const ex = exportFiles.get(f.source);
    ensure(
      ex &&
        JSON.stringify(ex.targets) === JSON.stringify(f.targets) &&
        JSON.stringify(ex.symbols.map((s: any) => s.upstream)) ===
          JSON.stringify(f.exports) &&
        JSON.stringify(f.exports) === JSON.stringify(old.exports),
      `导出映射不一致：${f.source}`,
    );
    if (f.batch) {
      const b = byBatch.get(f.batch);
      ensure(
        b &&
          b.module === f.module &&
          b.sources.includes(f.source) &&
          f.targets.length,
        `没有有效批次/目标：${f.source}`,
      );
      for (const t of f.targets)
        ensure(owned.get(t) === f.batch, `目标归属错误：${t}`);
    } else
      ensure(
        !f.module &&
          !f.targets.length &&
          ["excluded", "reference-only"].includes(f.disposition),
        `排除记录不完整：${f.source}`,
      );
  }
  const expected = analysis.files.filter(
    (f: any) =>
      inv.scopeRoots.some((r: string) => f.path.startsWith(r + "/")) ||
      inv.supportFiles.includes(f.path),
  );
  ensure(
    expected.length === files.size &&
      expected.every((f: any) => files.has(f.path)),
    "来源清单有遗漏或多余项",
  );
  for (const b of p.batches) {
    ensure(
      new Set(b.sources).size === b.sources.length &&
        b.sources.every((s: string) => files.get(s)?.batch === b.id),
      `来源批次冲突：${b.id}`,
    );
    const refs = new Set([...inv.referenceInputs, ...inv.files].map((r: any) => r.source));
    ensure(
      b.references.every((s: string) => refs.has(s)),
      `批次参考不存在：${b.id}`,
    );
  }
  ensure(
    !inv.importDecisions.some((e: any) => e.decision === "review-required"),
    "仍有未分类直接依赖",
  );
  const all = baselineEntries(inv);
  ensure(
    new Set(all.map((e: any) => e.source)).size === all.length,
    "来源/测试/资产重复",
  );
  for (const e of all) {
    safeRelative(e.source);
    ensure(/^[a-f0-9]{64}$/.test(e.sha256), `无效 hash：${e.source}`);
  }
  return {
    sourceFiles: files.size,
    portFiles: inv.files.filter((f: any) => f.batch).length,
    batches: byBatch.size,
    referenceInputs: inv.referenceInputs.length,
    referenceAssets: inv.referenceAssets.length,
  };
}

/** Read only watched source inputs. Never follow symlinks into another tree. */
export async function scanInputs(root: string, inventory: any) {
  const result = new Map<string, string>();
  const ignored = new Set([".git", "node_modules", "dist", ".cache"]);
  async function walk(name: string) {
    safeRelative(name);
    let st;
    try {
      st = await lstat(path.join(root, name));
    } catch (e) {
      if ((e as NodeJS.ErrnoException).code === "ENOENT") return;
      throw e;
    }
    ensure(!st.isSymbolicLink(), `来源路径为链接，需审查：${name}`);
    if (st.isDirectory()) {
      for (const child of (await readdir(path.join(root, name))).sort())
        if (!ignored.has(child)) await walk(name + "/" + child);
    } else if (st.isFile())
      result.set(name, sha256(await readFile(path.join(root, name))));
  }
  for (const name of [...inventory.watchRoots, ...inventory.watchFiles])
    await walk(name);
  return result;
}

export function diffInputs(
  bundle: Awaited<ReturnType<typeof loadBundle>>,
  current: Map<string, string>,
) {
  const entries = baselineEntries(bundle.inventory);
  const old = new Map<string, any>(entries.map((e: any) => [e.source, e]));
  const changed = entries
    .filter(
      (e: any) => current.has(e.source) && current.get(e.source) !== e.sha256,
    )
    .map((e: any) => e.source)
    .sort();
  const deleted = entries
    .filter((e: any) => !current.has(e.source))
    .map((e: any) => e.source)
    .sort();
  const added = [...current.keys()].filter((n) => !old.has(n)).sort();
  const changes = new Set<string>([...changed, ...deleted, ...added]);
  const scope = (n: string) =>
    n.includes("/pico3/") || n.includes("/testing/benchmark/")
      ? []
      : n.startsWith("packages/ai/") || n.startsWith("packages/telemetry/")
        ? ["ai"]
        : n.startsWith("packages/agent/src/harness/tools/") ||
            n.startsWith("packages/agent/src/harness/env/")
          ? ["tools"]
          : n.startsWith("packages/agent/") || n.startsWith("packages/chord/")
            ? ["core"]
            : ["ai", "core", "tools"];
  const direct = new Set<string>();
  for (const n of changes) {
    const e = old.get(n);
    for (const m of e ? (e.modules ?? (e.module ? [e.module] : [])) : scope(n))
      direct.add(m);
  }
  // Conservative reverse closure over the pinned complete import graph, including type edges.
  const impacted = new Set(changes);
  let progress = true;
  while (progress) {
    progress = false;
    for (const f of bundle.analysis.files)
      if (
        !impacted.has(f.path) &&
        f.imports.some((e: any) => impacted.has(e.target))
      ) {
        impacted.add(f.path);
        progress = true;
      }
  }
  const regressions = new Set(direct);
  for (const f of bundle.inventory.files)
    if (f.module && impacted.has(f.source)) regressions.add(f.module);
  progress = true;
  while (progress) {
    progress = false;
    for (const m of bundle.plan.modules)
      if (
        !regressions.has(m.id) &&
        m.dependsOn.some((d: string) => regressions.has(d))
      ) {
        regressions.add(m.id);
        progress = true;
      }
  }
  const order = ["ai", "core", "tools"];
  return {
    status: changes.size ? "replanning-required" : "no-watched-input-changes",
    baselineRevision: bundle.plan.revision,
    baselineKind: "planned-source-baseline-not-accepted-go",
    changed,
    added,
    deleted,
    possibleRenames: deleted.flatMap((from) =>
      added
        .filter((to) => current.get(to) === old.get(from).sha256)
        .map((to) => ({ from, to, confidence: "identical-content-only" })),
    ),
    directModules: order.filter((m) => direct.has(m)),
    regressionModules: order.filter((m) => regressions.has(m)),
    unclassified: added.filter((n) => scope(n).length),
    excludedAdditions: added.filter((n) => !scope(n).length),
    generatedCatalogChanges: [...changes]
      .filter((n) => n.startsWith("packages/ai/src/providers/data/"))
      .sort(),
    affectedTargets: [
      ...new Set(
        bundle.inventory.files
          .filter((f: any) => impacted.has(f.source))
          .flatMap((f: any) => f.targets),
      ),
    ].sort(),
    note: "只读比较；新增源码需重新分析/规划，报告不证明新版本身份，不更新接受基线，也不执行迁移。",
  };
}
