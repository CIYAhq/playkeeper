// From coss ui (https://coss.com/ui), MIT licensed; see THIRD_PARTY_NOTICES.
import type React from "react";
import { cn } from "@/lib/utils";

// Playkeeper: a gentle pulse on a tint that shows on white cards and the chalk background alike.
export function Skeleton({
  className,
  ...props
}: React.ComponentProps<"div">): React.ReactElement {
  return (
    <div
      aria-hidden="true"
      className={cn("animate-skeleton rounded-sm bg-black/[.06]", className)}
      data-slot="skeleton"
      {...props}
    />
  );
}
