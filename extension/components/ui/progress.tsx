// Progress is a thin sync bar. With a known fraction it fills determinately;
// without one it sweeps (gusset-sweep) so a slow transfer visibly reads as
// "still going," not stalled. Reduced motion swaps the sweep for a steady
// tinted bar — the decrementing chunk count still conveys liveness.

export function Progress({ fraction }: { fraction: number | null }) {
  const indeterminate = fraction === null;
  return (
    <div
      className="h-1.5 w-full overflow-hidden rounded-full bg-[var(--panel-2)]"
      role="progressbar"
      aria-valuemin={0}
      aria-valuemax={indeterminate ? undefined : 100}
      aria-valuenow={indeterminate ? undefined : Math.round(fraction * 100)}
    >
      {indeterminate ? (
        <div className="gusset-sweep h-full w-1/3 rounded-full bg-[var(--info)]" />
      ) : (
        <div
          className="h-full rounded-full bg-[var(--info)] transition-[width] duration-500"
          style={{ width: `${Math.round(fraction * 100)}%` }}
        />
      )}
    </div>
  );
}
