import releaseManifest from "../../../cmd/dbterm/releases.txt?raw";

const releaseLine = releaseManifest
  .split(/\r?\n/)
  .find((line) => line.trim() && !line.trimStart().startsWith("#"));
const [version, name] = releaseLine?.split("|").map((value) => value.trim()) ?? [];

if (!version || !name) throw new Error("cmd/dbterm/releases.txt has no valid current release");

export const currentRelease = {
  version,
  name,
  label: `v${version} · ${name}`,
  url: `https://github.com/shreyam1008/dbterm/releases/tag/v${version}`
};
