import { type ClassValue, clsx } from "clsx";
import { twMerge } from "tailwind-merge";

// cn merges Tailwind classes, resolving conflicts (the shadcn/ui convention).
export function cn(...inputs: ClassValue[]): string {
  return twMerge(clsx(inputs));
}
