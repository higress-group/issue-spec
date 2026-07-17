type MarkdownNode = {
  type: string;
  value?: string;
  url?: string;
  children?: MarkdownNode[];
};

const excludedParents = new Set(["link", "linkReference", "inlineCode", "code"]);

export function remarkMentions() {
  return (tree: MarkdownNode) => transformChildren(tree);
}

function transformChildren(parent: MarkdownNode) {
  if (!parent.children || excludedParents.has(parent.type)) return;
  const transformed: MarkdownNode[] = [];
  for (const child of parent.children) {
    if (child.type === "text" && child.value) transformed.push(...mentionNodes(child.value));
    else {
      transformChildren(child);
      transformed.push(child);
    }
  }
  parent.children = transformed;
}

function mentionNodes(value: string): MarkdownNode[] {
  const result: MarkdownNode[] = [];
  let plainStart = 0;
  for (let index = 0; index < value.length; index += 1) {
    if (value[index] !== "@") continue;
    const prior = index > 0 ? value[index - 1] : "";
    if (prior && /[\p{L}\p{N}_@/]/u.test(prior)) continue;
    let end = index + 1;
    while (end < value.length && /[A-Za-z0-9-]/.test(value[end])) end += 1;
    const login = value.slice(index + 1, end);
    if (!login || login.length > 64 || login.startsWith("-") || login.endsWith("-") ||
      (end < value.length && /[_\p{L}\p{N}-]/u.test(value[end]))) continue;
    if (plainStart < index) result.push({ type: "text", value: value.slice(plainStart, index) });
    result.push({ type: "link", url: `/users/${encodeURIComponent(login.toLocaleLowerCase())}`,
      children: [{ type: "text", value: `@${login}` }] });
    plainStart = end;
    index = end - 1;
  }
  if (plainStart < value.length) result.push({ type: "text", value: value.slice(plainStart) });
  return result.length ? result : [{ type: "text", value }];
}
