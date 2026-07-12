const markerPatterns = [
  /^<!-- issue-spec:issue=(?:proposal|design|implement) change=[A-Za-z0-9][A-Za-z0-9._/-]* version=\d+ -->$/,
  /^<!-- issue-spec:type=[A-Z][A-Z0-9_-]* id=[A-Z][A-Z0-9_-]*-\d+ version=\d+ -->$/,
  /^<!-- issue-spec:issue-update-summary version=\d+ -->$/,
];

export function stripIssueSpecMarkersForRender(source: string): string {
  let fence: "`" | "~" | undefined;
  return source.split("\n").filter((line) => {
    const opening = line.match(/^\s*(`{3,}|~{3,})/);
    if (opening) {
      const character = opening[1][0] as "`" | "~";
      if (!fence) fence = character;
      else if (fence === character) fence = undefined;
      return true;
    }
    return Boolean(fence) || !markerPatterns.some((pattern) => pattern.test(line.trim()));
  }).join("\n");
}
