import type { ReactNode } from "react";
import { cn } from "@/lib/utils";

// Badge is a small status pill. The color classes are passed in (from
// lib/display) so the status vocabulary lives in one place.
export function Badge({
  className,
  title,
  children,
}: {
  className?: string;
  title?: string;
  children: ReactNode;
}) {
  return (
    <span
      title={title}
      className={cn(
        "inline-flex items-center rounded px-1.5 py-0.5 text-xs font-medium",
        className,
      )}
    >
      {children}
    </span>
  );
}
