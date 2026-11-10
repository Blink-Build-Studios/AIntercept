# Canvas runtime

`support.js` is Claude Design's artboard runtime (`artifact-type/dc-runtime.js`
from the published canvas "aintercept — Person & session pages", sha256
b795ee21261f2e7da1b10cb25b4a4cfa275d1b96165f89396f1fed426e191844; React 18.3.1
bundled). Served next to the `.dc.html` artboards, it renders them in any
browser exactly as the canvas does, which is what `make parity` measures
against.

Not ours: exclude this directory from any public export (AIntercept).
