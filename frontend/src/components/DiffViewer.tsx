// Renders a unified diff with +/- line coloring.

export function DiffViewer({ diff }: { diff: string }) {
  const lines = diff.split("\n");
  return (
    <pre className="diff">
      {lines.map((line, i) => {
        let cls = "diff-ctx";
        if (line.startsWith("+") && !line.startsWith("+++")) cls = "diff-add";
        else if (line.startsWith("-") && !line.startsWith("---")) cls = "diff-del";
        else if (line.startsWith("@@")) cls = "diff-hunk";
        else if (line.startsWith("+++") || line.startsWith("---")) cls = "diff-file";
        return (
          <div key={i} className={cls}>
            {line || " "}
          </div>
        );
      })}
    </pre>
  );
}
