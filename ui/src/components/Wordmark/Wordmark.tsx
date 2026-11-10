import "./Wordmark.css";

export type WordmarkProps = { size?: "appBar" | "large" };

/** AIntercept (handoff §11H.1): weight is the only distinction, AI 600 and ntercept 400. */
export function Wordmark({ size = "appBar" }: WordmarkProps) {
  return (
    <span className={`wordmark ${size === "large" ? "wordmark-large" : ""}`}>
      <span className="wordmark-ai">AI</span>
      <span>ntercept</span>
    </span>
  );
}
