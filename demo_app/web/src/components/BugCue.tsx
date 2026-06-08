// BugCue is the visual marker the user asked for: every control that intentionally
// triggers a problem shows it (paired with an amber ring on the card).
export function BugCue() {
  return (
    <span className="bug-cue" title="This action intentionally triggers a production issue">
      ⚠ triggers an issue
    </span>
  );
}
