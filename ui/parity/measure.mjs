// Runs in the browser (page.evaluate): a structural, numeric description of an
// element, so two renderings can be compared property by property, never by pixels.
export function describeElement(root) {
  const STYLE = [
    "display", "font-family", "font-size", "font-weight", "line-height", "letter-spacing", "text-transform",
    "text-align", "color", "background-color", "opacity", "border-top-width", "border-right-width",
    "border-bottom-width", "border-left-width", "border-top-style", "border-top-color", "border-right-color",
    "border-bottom-color", "border-left-color", "border-top-left-radius", "padding-top", "padding-right",
    "padding-bottom", "padding-left", "column-gap", "row-gap", "white-space", "outline-style", "outline-width",
    "outline-color", "outline-offset",
  ];
  const r0 = root.getBoundingClientRect();
  const styles = (el) => {
    const cs = getComputedStyle(el);
    const out = {};
    for (const p of STYLE) out[p] = cs.getPropertyValue(p);
    out["font-family"] = out["font-family"].split(",")[0].replace(/["']/g, "").trim();
    return out;
  };
  const box = (el) => {
    const r = el.getBoundingClientRect();
    return { x: +(r.left - r0.left).toFixed(2), y: +(r.top - r0.top).toFixed(2), w: +r.width.toFixed(2), h: +r.height.toFixed(2) };
  };
  // The canvas runtime wraps interpolated values in span.sc-interp; treat it as
  // transparent, so its text belongs to the parent as it does in our DOM.
  const interp = (n) => n.nodeType === 1 && n.classList.contains("sc-interp");
  const textOf = (el) => [...el.childNodes].map((n) => (n.nodeType === 3 ? n.textContent : interp(n) ? textOf(n) : "")).join("");
  const ownText = (el) => textOf(el).replace(/\s+/g, " ").trim();
  const visible = (el) => {
    const r = el.getBoundingClientRect();
    return r.width > 0 && r.height > 0 && getComputedStyle(el).visibility !== "hidden";
  };
  const texts = [];
  const marks = [];
  for (const el of root.querySelectorAll("*")) {
    if (!visible(el) || ["SCRIPT", "STYLE"].includes(el.tagName) || el.parentElement?.closest("svg") || interp(el) || el.closest("[data-spec-illustrative]")) continue;
    if (el.tagName.toLowerCase() === "svg") {
      marks.push({ box: box(el), style: {} });
      continue;
    }
    const t = ownText(el);
    if (t) texts.push({ text: t, box: box(el), style: styles(el) });
    else if (el.children.length === 0) {
      const cs = getComputedStyle(el);
      const painted = cs.backgroundColor !== "rgba(0, 0, 0, 0)" || parseFloat(cs.borderTopWidth) > 0;
      if (painted) marks.push({ box: box(el), style: styles(el) });
    }
  }
  return { text: ownText(root), box: box(root), style: styles(root), texts, marks };
}
