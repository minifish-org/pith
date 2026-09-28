/** Optional migration tooling. Pith's future Go build will not require Node. */
import { createHash } from "node:crypto";
import { execFile } from "node:child_process";
import { promisify } from "node:util";
import { fileURLToPath } from "node:url";
import {
  lstat,
  mkdir,
  mkdtemp,
  readFile,
  readdir,
  readlink,
  rename,
  rm,
  writeFile,
} from "node:fs/promises";
import path from "node:path";

const root = fileURLToPath(new URL("../", import.meta.url));
const pin = JSON.parse(
  await readFile(new URL("./upstream.json", import.meta.url), "utf8"),
);
const digest = (data: Buffer | string, algorithm = "sha256") =>
  createHash(algorithm).update(data).digest("hex");
const run = promisify(execFile);
const archive = path.join(root, pin.archiveFile),
  source = path.join(root, pin.sourceDir),
  treeFile = path.join(root, pin.treeFile);
type Blob = { path: string; mode: string; sha: string };

async function exists(file: string) {
  try {
    await lstat(file);
    return true;
  } catch (e) {
    if ((e as NodeJS.ErrnoException).code === "ENOENT") return false;
    throw e;
  }
}
function safeRelative(name: string) {
  if (
    path.isAbsolute(name) ||
    name.includes("\\") ||
    name.split("/").includes("..")
  )
    throw Error(`Unsafe archive member: ${name}`);
}
async function fetchBytes(url: string, limit: number) {
  const response = await fetch(url, {
    headers: { "User-Agent": "Pith-source-audit" },
    signal: AbortSignal.timeout(60000),
  });
  if (!response.ok || !response.body)
    throw Error(`Download failed: HTTP ${response.status}`);
  let total = 0;
  const chunks: Buffer[] = [];
  for await (const chunk of response.body) {
    total += chunk.length;
    if (total > limit) throw Error("Download exceeds expected size");
    chunks.push(Buffer.from(chunk));
  }
  return Buffer.concat(chunks);
}
async function verifyArchive() {
  const bytes = await readFile(archive);
  if (bytes.length !== pin.archiveBytes || digest(bytes) !== pin.archiveSha256)
    throw Error(
      "Archive checksum/size mismatch; preserve it for inspection instead of overwriting",
    );
}
function checkTree(tree: { sha: string; blobs: Blob[] }) {
  if (
    tree.sha !== pin.gitTreeSha ||
    tree.blobs.length !== pin.gitBlobCount ||
    digest(JSON.stringify(tree.blobs)) !== pin.gitBlobManifestSha256
  )
    throw Error("Git blob manifest does not match the pinned manifest");
}
async function fetchSource() {
  await mkdir(path.dirname(archive), { recursive: true });
  if (!(await exists(archive))) {
    const bytes = await fetchBytes(pin.archiveUrl, pin.archiveBytes);
    if (
      digest(bytes) !== pin.archiveSha256 ||
      bytes.length !== pin.archiveBytes
    )
      throw Error("Downloaded archive does not match the pin");
    await writeFile(archive, bytes, { flag: "wx" });
  }
  await verifyArchive();
  if (!(await exists(treeFile))) {
    const data = JSON.parse(
      (
        await fetchBytes(
          `${pin.repository.replace("https://github.com/", "https://api.github.com/repos/")}/git/trees/${pin.commit}?recursive=1`,
          4 * 1024 * 1024,
        )
      ).toString(),
    );
    if (
      data.truncated ||
      data.tree.some((e: { type: string }) => e.type === "commit")
    )
      throw Error("Truncated tree or submodule requires explicit handling");
    const blobs: Blob[] = data.tree
      .filter((e: { type: string }) => e.type === "blob")
      .map(({ path, mode, sha }: Blob) => ({ path, mode, sha }))
      .sort((a: Blob, b: Blob) =>
        a.path < b.path ? -1 : a.path > b.path ? 1 : 0,
      );
    const tree = { sha: data.sha, blobs };
    checkTree(tree);
    await writeFile(treeFile, JSON.stringify(tree, null, 2) + "\n", {
      flag: "wx",
    });
  }
  if (!(await exists(source))) {
    const tree: { sha: string; blobs: Blob[] } = JSON.parse(
      await readFile(treeFile, "utf8"),
    );
    checkTree(tree);
    // This pinned archive contains regular files only. Don't silently expand this
    // extraction policy if a future version introduces symlinks or submodules.
    if (tree.blobs.some((e) => !["100644", "100755"].includes(e.mode)))
      throw Error("This source pin needs a reviewed link extraction policy");
    const listing = await run("tar", ["-tzf", archive], {
      maxBuffer: 4 * 1024 * 1024,
    });
    const names = listing.stdout.trim().split("\n");
    const prefix = names[0].split("/")[0] + "/";
    for (const name of names) {
      safeRelative(name);
      if (!name.startsWith(prefix)) throw Error("Unexpected archive root");
    }
    const temp = await mkdtemp(path.join(path.dirname(source), "extract-"));
    try {
      await run("tar", ["-xzf", archive, "-C", temp, "--strip-components=1"]);
      await rename(temp, source);
    } finally {
      await rm(temp, { recursive: true, force: true });
    }
  }
}
async function verifySource() {
  await verifyArchive();
  const tree: { sha: string; blobs: Blob[] } = JSON.parse(
    await readFile(treeFile, "utf8"),
  );
  checkTree(tree);
  if ((await lstat(source)).isSymbolicLink())
    throw Error("Source root cannot be a symlink");
  const actual = new Set<string>();
  async function walk(dir: string) {
    for (const e of await readdir(path.join(source, dir), {
      withFileTypes: true,
    })) {
      const name = path.posix.join(dir, e.name);
      if (e.isDirectory()) await walk(name);
      else actual.add(name);
    }
  }
  await walk("");
  const normalized: string[] = [];
  for (const blob of tree.blobs) {
    safeRelative(blob.path);
    const file = path.join(source, blob.path);
    const stat = await lstat(file);
    if (stat.isSymbolicLink() !== (blob.mode === "120000"))
      throw Error(`Unexpected file type: ${blob.path}`);
    let data = stat.isSymbolicLink()
      ? Buffer.from(await readlink(file))
      : await readFile(file);
    const gitHash = (value: Buffer) =>
      digest(
        Buffer.concat([Buffer.from(`blob ${value.length}\0`), value]),
        "sha1",
      );
    if (
      gitHash(data) !== blob.sha &&
      pin.normalization.files.includes(blob.path)
    ) {
      data = Buffer.from(data.toString().replaceAll("\r\n", "\n"));
      normalized.push(blob.path);
    }
    if (gitHash(data) !== blob.sha)
      throw Error(`Git blob mismatch: ${blob.path}`);
    actual.delete(blob.path);
  }
  if (actual.size)
    throw Error(`Unexpected source files: ${[...actual].join(", ")}`);
  const report = {
    commit: pin.commit,
    files: tree.blobs.length,
    archiveSha256: pin.archiveSha256,
    normalized,
    verified: true,
  };
  await writeFile(
    path.join(root, ".cache/pi/source-verification.json"),
    JSON.stringify(report, null, 2) + "\n",
  );
  console.log(JSON.stringify(report, null, 2));
}
try {
  const command = process.argv[2];
  if (command === "fetch") await fetchSource();
  else if (command !== "verify") throw Error("Usage: source.mts fetch|verify");
  await verifySource();
} catch (e) {
  console.error(e instanceof Error ? e.message : String(e));
  process.exitCode = 1;
}
