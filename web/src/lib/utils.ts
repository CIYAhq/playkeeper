// From coss ui (https://coss.com/ui), MIT licensed; see THIRD_PARTY_NOTICES.
import { type ClassValue, clsx } from "clsx";
import { twMerge } from "tailwind-merge";

export function cn(...inputs: ClassValue[]): string {
  return twMerge(clsx(inputs));
}
