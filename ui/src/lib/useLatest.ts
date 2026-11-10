import { useLayoutEffect, useRef } from "react";

/**
 * A ref holding the latest value. Callbacks that read it stay valid without
 * being dependencies, so work memoized on data (a timeline's lanes) doesn't
 * redo itself each time a parent passes new arrow functions.
 */
export function useLatest<T>(value: T) {
  const ref = useRef(value);
  useLayoutEffect(() => {
    ref.current = value;
  });
  return ref;
}
