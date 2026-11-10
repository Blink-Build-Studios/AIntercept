import { useParams } from "react-router-dom";
import { usePersonPage } from "../../api/hooks/usePersonPage";
import { useDocumentTitle } from "../../lib/useDocumentTitle";
import { PersonScreen } from "../../screens/PersonScreen/PersonScreen";

/** /people/:personId (handoff §7.2): the person page's data and URL state, into PersonScreen. */
export function PersonPage() {
  const { personId = "" } = useParams();
  const p = usePersonPage(personId);
  const person = p.person.data;
  useDocumentTitle(person && (person.personId === "unattributed" ? "Unattributed traffic" : (person.name ?? "name unknown")));
  return <PersonScreen {...p} />;
}
