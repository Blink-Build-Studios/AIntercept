import { useParams } from "react-router-dom";
import { useSessionPage } from "../../api/hooks/useSessionPage";
import { useDocumentTitle } from "../../lib/useDocumentTitle";
import { SessionScreen } from "../../screens/SessionScreen/SessionScreen";

/** `/sessions/:sessionId` (handoff §7.3): the session's data and URL state, into SessionScreen. */
export function SessionPage() {
  const { sessionId = "" } = useParams();
  const p = useSessionPage(sessionId);
  const s = p.session.data;
  useDocumentTitle(s && (s.repo ?? s.label.text), !!s?.live);
  return <SessionScreen {...p} />;
}
