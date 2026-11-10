import { useEffect } from "react";

const BRAND = "AIntercept";

/** The tab title (handoff §11H.1): the page first, the brand last; just the brand when there's no page name yet. */
export function documentTitle(page?: string | null, live = false): string {
  return page ? `${live ? "● " : ""}${page} · ${BRAND}` : BRAND;
}

export function useDocumentTitle(page?: string | null, live = false) {
  const title = documentTitle(page, live);
  useEffect(() => {
    document.title = title;
  }, [title]);
}
