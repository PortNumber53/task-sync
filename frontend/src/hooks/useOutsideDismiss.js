import * as React from 'react';

/**
 * useOutsideDismiss
 * Adds document listeners to dismiss when clicking outside any provided refs
 * or pressing Escape. Only active when `enabled` is true.
 *
 * @param {React.RefObject<HTMLElement>|Array<React.RefObject<HTMLElement>>} refs
 * @param {() => void} onDismiss
 * @param {boolean} enabled
 */
export function useOutsideDismiss(refs, onDismiss, enabled = true) {
  React.useEffect(() => {
    if (!enabled) return;

    const refList = Array.isArray(refs) ? refs : [refs];

    function isTargetInside(target) {
      return refList.some((r) => r?.current && r.current.contains(target));
    }

    function onDocMouseDown(e) {
      if (!(e.target instanceof Node)) return;
      if (!isTargetInside(e.target)) onDismiss?.();
    }

    function onKey(e) {
      if (e.key === 'Escape') onDismiss?.();
    }

    document.addEventListener('mousedown', onDocMouseDown);
    document.addEventListener('keydown', onKey);

    return () => {
      document.removeEventListener('mousedown', onDocMouseDown);
      document.removeEventListener('keydown', onKey);
    };
  }, [enabled, onDismiss, refs]);
}
