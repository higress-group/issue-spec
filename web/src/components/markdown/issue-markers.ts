const markerPatterns = [
  /^<!-- issue-spec:issue=(?:proposal|design|implement) change=[A-Za-z0-9][A-Za-z0-9._/-]* version=\d+ -->$/,
  /^<!-- issue-spec:type=[A-Z][A-Z0-9_-]* id=[A-Z][A-Z0-9_-]*-\d+ version=\d+ -->$/,
  /^<!-- issue-spec:issue-update-summary version=\d+ -->$/,
];

type Fence = { character: "`" | "~"; length: number };

function fenceRun(line: string) {
  let index = 0;
  while (index < line.length && line[index] === " " && index < 4) index++;
  if (index > 3 || (line[index] !== "`" && line[index] !== "~")) return null;
  const character = line[index] as Fence["character"];
  let runEnd = index;
  while (line[runEnd] === character) runEnd++;
  const length = runEnd - index;
  return length >= 3 ? { character, length, rest: line.slice(runEnd) } : null;
}

export function stripIssueSpecMarkersForRender(source: string): string {
  let fence: Fence | undefined;
  return source.split("\n").filter((line) => {
    const run = fenceRun(line);
    if (!fence && run && (run.character !== "`" || !run.rest.includes("`"))) {
      fence = run;
      return true;
    }
    if (fence && run?.character === fence.character && run.length >= fence.length && /^[ \t\r]*$/.test(run.rest)) {
      fence = undefined;
      return true;
    }
    return Boolean(fence) || !markerPatterns.some((pattern) => pattern.test(line.trim()));
  }).join("\n");
}
