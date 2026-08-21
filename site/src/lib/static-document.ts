export const staticDocument = (body: string, contentType: string) =>
  new Response(body.endsWith("\n") ? body : `${body}\n`, {
    headers: {
      "Content-Type": `${contentType}; charset=utf-8`,
      "Access-Control-Allow-Origin": "*"
    }
  });

const readmeLinkTargets = new Map([
  ["LICENSE", "https://github.com/shreyam1008/dbterm/blob/main/LICENSE"],
  ["docs/user-guide.md", "https://dbterm.shreyam1008.com.np/guide.md"],
  ["docs/backup.md", "https://dbterm.shreyam1008.com.np/backup.md"],
  ["docs/marketing-plan.md", "https://github.com/shreyam1008/dbterm/blob/main/docs/marketing-plan.md"],
  ["docs/distribution-log.md", "https://github.com/shreyam1008/dbterm/blob/main/docs/distribution-log.md"],
  ["docs/domain-release.md", "https://github.com/shreyam1008/dbterm/blob/main/docs/domain-release.md"],
  [
    "docs/project-reference.md#file-map",
    "https://github.com/shreyam1008/dbterm/blob/main/docs/project-reference.md#file-map"
  ]
]);

export const rewriteReadmeLinksForSite = (body: string) =>
  body.replace(/\]\(([^)\s]+)\)/g, (link, target: string) => {
    const publicTarget = readmeLinkTargets.get(target);
    return publicTarget ? `](${publicTarget})` : link;
  });
